// 本文件实现「模板驱动的自定义异步任务上游适配器」。
//
// 意图（Why）：
//
//	图像 / 视频生成厂商的接口形态高度分散——提交路径、任务号字段、状态字段、
//	结果字段、鉴权头各不相同。若逐个硬编码，每接一家就要改一次代码，
//	而厂商接口还在不断变化，维护成本不可控。
//
//	因此本适配器把「协议差异」下沉为渠道配置：站长在渠道的扩展配置里填写
//	提交/查询路径与各字段的点路径，适配器据此完成协议翻译。
//	它只实现「提交 → 轮询 → 取结果」这一通用机制，不含任何具体厂商的私有约定
//	（路径与字段全部来自配置，绝不杜撰厂商 API 细节）。
//
//	设计原则与内置适配器一致：只做协议翻译，不碰计费、不碰数据库、不碰路由。
//
// 流转（Flow）：
//
//	TaskService.Submit → customAsyncProvider.Submit
//	  └─ 读渠道扩展配置(submit_path/id_path) → 构造请求(按渠道鉴权) → 上游
//	     → 取任务号 → 返回"排队中"
//	TaskService.PollOnce → customAsyncProvider.Poll
//	  └─ 读渠道扩展配置(poll_path/status_path/result_url_path...) → 上游
//	     → 按状态映射与点路径解析 → 返回状态/进度/结果/错误(已脱敏)
//
// 扩展（Extend）：
//
//	新增字段映射：在 pickChannelExtra 支持的键之外，直接读 ch.ExtraConfig 即可，
//	无需改本文件（这也是"模板驱动"的意义）。
//	注意两点（与内置适配器共同的红线）：
//	  - 状态映射必须保守：无法识别的状态一律当作"进行中"，绝不能因为遇到
//	    一个新状态词就把任务误判为失败（误判失败会立即退还额度又给了结果）；
//	  - Poll 返回的 Error 必须脱敏，不得带上游地址与密钥。
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/LTZY-ACU/ltzy-api/internal/channeltype"
	"github.com/LTZY-ACU/ltzy-api/internal/model"
)

// 自定义异步适配器的配置键（写在渠道扩展配置 ch.ExtraConfig 里）。
//
// 集中定义的原因：它们同时要被适配器读取、被后台表单渲染、被站长理解，
// 散落成魔法字符串极易写错（例如把 async_submit_path 拼成 submit_path）。
const (
	// cfgCustomSubmitPath 提交路径（必填），如 /v1/video/generations。
	cfgCustomSubmitPath = "async_submit_path"
	// cfgCustomSubmitMethod 提交方法（可选，默认 POST）。
	cfgCustomSubmitMethod = "async_submit_method"
	// cfgCustomPollPath 查询路径（必填），支持 {id} 占位符，如 /v1/tasks/{id}。
	cfgCustomPollPath = "async_poll_path"
	// cfgCustomIDPath 任务号在提交响应中的点路径（可选），如 data.id。
	// 缺省时回退为顶层 id / task_id / taskId。
	cfgCustomIDPath = "async_id_path"
	// cfgCustomStatusPath 状态字段点路径（可选），如 data.status。
	cfgCustomStatusPath = "async_status_path"
	// cfgCustomProgressPath 进度字段点路径（可选），如 data.progress。
	cfgCustomProgressPath = "async_progress_path"
	// cfgCustomResultURLPath 结果地址字段点路径（可选），如 output.images.0.url。
	cfgCustomResultURLPath = "async_result_url_path"
	// cfgCustomErrorPath 失败原因字段点路径（可选），如 data.fail_reason。
	cfgCustomErrorPath = "async_error_path"
	// cfgCustomStatusMap 状态词映射（可选），如 completed=success,running=running。
	cfgCustomStatusMap = "async_status_map"
)

// taskErrorMaxBytes 是写入错误字段的最大字节数。
//
// 截断的理由：上游的错误体可能是整页 HTML 或超长堆栈，原样落库既占空间
// 又可能夹带敏感信息；对使用者而言前 500 字节已足够定位问题。
const taskErrorMaxBytes = 500

// customAsyncProvider 是模板驱动的通用异步任务适配器。
//
// 它不绑定任何具体厂商：一切路径与字段都来自渠道扩展配置。
// 作为"通用兜底"注册在内置适配器之后——只有当没有更专用的适配器时，
// 才会被自动选中（站长通常通过显式指定 provider=custom_async 来使用它）。
type customAsyncProvider struct {
	client *http.Client
}

// Name 返回适配器名。
func (p *customAsyncProvider) Name() string { return ProviderCustomAsync }

// Supports 声明支持的任务类别。
//
// 三类全支持：它的能力由渠道配置决定（填了视频路径就能跑视频），
// 作为兜底适配器不应在类别层面自我设限。
func (p *customAsyncProvider) Supports(kind model.TaskKind) bool {
	switch kind {
	case model.TaskKindImage, model.TaskKindVideo, model.TaskKindMusic:
		return true
	default:
		return false
	}
}

// Submit 提交任务：按配置拼接提交路径，发请求并取回任务号。
func (p *customAsyncProvider) Submit(ctx context.Context, req *TaskRequest, ch *model.Channel, apiKey string) (*TaskSubmitResult, error) {
	submitPath := channelExtra(ch, cfgCustomSubmitPath)
	if submitPath == "" {
		return nil, fmt.Errorf("渠道未配置 %s（自定义异步上游的提交路径）", cfgCustomSubmitPath)
	}
	method := strings.ToUpper(channelExtra(ch, cfgCustomSubmitMethod))
	if method == "" {
		method = http.MethodPost
	}

	// 请求体：原样透传客户端参数，并补上统一的 prompt / model
	// （与内置适配器一致：客户端可能用不同的字段名传提示词，这里统一）。
	payload := make(map[string]any, len(req.Params)+2)
	for key, value := range req.Params {
		payload[key] = value
	}
	payload["prompt"] = req.Prompt
	payload["model"] = req.Model

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("构造请求体失败: %w", err)
	}

	upReq, err := newCustomTaskRequest(ctx, method,
		joinUpstreamPath(ch.BaseURL, submitPath), body, ch, apiKey)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(upReq)
	if err != nil {
		return nil, fmt.Errorf("请求上游失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxTaskResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("读取上游响应失败: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		// 错误文案先脱敏再返回：上游可能把地址/密钥回显在错误里。
		return nil, fmt.Errorf("上游返回 HTTP %d：%s",
			resp.StatusCode, sanitizeTaskError(extractErrorMessage(raw), ch, apiKey))
	}

	root, ok := decodeJSONAny(raw)
	if !ok {
		return nil, fmt.Errorf("上游响应不是合法 JSON")
	}

	upstreamID := extractCustomTaskID(root, channelExtra(ch, cfgCustomIDPath))
	if upstreamID == "" {
		return nil, fmt.Errorf("上游未返回任务号（请检查 %s 配置）", cfgCustomIDPath)
	}

	return &TaskSubmitResult{
		UpstreamID: upstreamID,
		Status:     model.TaskStatusQueued,
	}, nil
}

// Poll 查询任务状态：按配置拼接查询路径，并解析状态/进度/结果/错误。
func (p *customAsyncProvider) Poll(ctx context.Context, task *model.Task, ch *model.Channel, apiKey string) (*TaskPollResult, error) {
	pollPath := channelExtra(ch, cfgCustomPollPath)
	if pollPath == "" {
		return nil, fmt.Errorf("渠道未配置 %s（自定义异步上游的查询路径）", cfgCustomPollPath)
	}
	// {id} 是唯一的占位符；用 PathEscape 防止任务号里的特殊字符改变路径结构。
	fullURL := joinUpstreamPath(ch.BaseURL,
		strings.ReplaceAll(pollPath, "{id}", url.PathEscape(task.UpstreamID)))

	upReq, err := newCustomTaskRequest(ctx, http.MethodGet, fullURL, nil, ch, apiKey)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(upReq)
	if err != nil {
		return nil, fmt.Errorf("查询上游任务失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxTaskResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("读取上游响应失败: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}

	root, ok := decodeJSONAny(raw)
	if !ok {
		return nil, fmt.Errorf("上游响应不是合法 JSON")
	}

	overrides := parseStatusMap(channelExtra(ch, cfgCustomStatusMap))
	rawStatus, _ := lookupDotPath(root, channelExtra(ch, cfgCustomStatusPath))
	status := mapCustomStatus(rawStatus, overrides)

	progress := 0
	if value, found := lookupDotPath(root, channelExtra(ch, cfgCustomProgressPath)); found {
		progress = parsePercent(value)
	}

	resultURL, _ := lookupDotPath(root, channelExtra(ch, cfgCustomResultURLPath))

	result := &TaskPollResult{
		Status:     status,
		Progress:   progress,
		ResultURL:  resultURL,
		ResultData: string(raw),
	}
	if status == model.TaskStatusFailed {
		reason, _ := lookupDotPath(root, channelExtra(ch, cfgCustomErrorPath))
		if strings.TrimSpace(reason) == "" {
			reason = "上游任务失败"
		}
		result.Error = sanitizeTaskError(reason, ch, apiKey)
	}
	if status == model.TaskStatusSucceeded && result.Progress == 0 {
		result.Progress = 100
	}
	return result, nil
}

// newCustomTaskRequest 构造自定义异步上游请求，并按渠道鉴权方式注入凭据。
//
// 鉴权复用转发链路的 applyUpstreamAuth（Bearer / api-key 头 / x-api-key / 查询参数），
// 依据渠道的 type_key 决定；type_key 为空或未登记时回退为 Bearer——
// 与任务链路既有适配器（newTaskRequest）的默认行为保持一致，历史渠道行为不变。
func newCustomTaskRequest(ctx context.Context, method, fullURL string, body []byte, ch *model.Channel, apiKey string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reader)
	if err != nil {
		return nil, fmt.Errorf("relay: 构造上游任务请求失败: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		query := req.URL.Query()
		if _, err := applyUpstreamAuth(authSpecForChannel(ch), apiKey, req.Header, query); err != nil {
			return nil, err
		}
		if len(query) > 0 {
			req.URL.RawQuery = query.Encode()
		}
	}
	return req, nil
}

// authSpecForChannel 返回渠道对应的类型定义，用于决定鉴权方式。
//
// 找不到时回退为一个"Bearer"的合成类型：宁可走最常见的鉴权方式，
// 也不要因缺少类型登记而无法发出请求（那样会让自定义上游完全不可用）。
func authSpecForChannel(ch *model.Channel) channeltype.Type {
	if ch != nil {
		if spec, ok := channeltype.Find(ch.TypeKey); ok {
			return spec
		}
	}
	return channeltype.Type{Key: "custom_async", AuthMode: channeltype.AuthBearer}
}

// channelExtra 读取渠道扩展配置项的原始值（去空白，nil 安全）。
func channelExtra(ch *model.Channel, key string) string {
	if ch == nil {
		return ""
	}
	return strings.TrimSpace(ch.ExtraConfig[key])
}

// joinUpstreamPath 拼接基础地址与路径，容忍两侧多余（或缺失）的斜杠。
func joinUpstreamPath(base, path string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/") + "/" + strings.TrimLeft(strings.TrimSpace(path), "/")
}

// decodeJSONAny 把响应体解析为通用的 any 结构（数字保留为 json.Number）。
//
// 用 json.Number 而非 float64 的原因：任务号 / 进度常是数字，
// float64 会把大整数渲染成 1.2e+18 这类科学计数法，导致任务号失真。
func decodeJSONAny(raw []byte) (any, bool) {
	var root any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, false
	}
	return root, true
}

// lookupDotPath 按"点路径"从 JSON 结构中取值，取不到时返回 ("", false)（不报错）。
//
// 路径格式：以 . 分隔的键或数组下标，如 data.id、output.images.0.url。
// 之所以返回空而不报错：字段缺失是"配置可能没写全"的常态，
// 报错会让一次不完整的配置直接导致任务失败；交由调用方决定如何降级。
func lookupDotPath(root any, path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	current := root
	for _, segment := range strings.Split(path, ".") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return "", false
		}
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[segment]
			if !ok {
				return "", false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return "", false
			}
			current = node[index]
		default:
			return "", false
		}
	}
	return stringifyScalar(current)
}

// stringifyScalar 把叶子节点转成字符串；非标量（对象/数组/空值）返回空。
func stringifyScalar(value any) (string, bool) {
	switch node := value.(type) {
	case string:
		text := strings.TrimSpace(node)
		return text, text != ""
	case json.Number:
		return node.String(), true
	case bool:
		return strconv.FormatBool(node), true
	default:
		return "", false
	}
}

// extractCustomTaskID 从提交响应里取任务号。
//
// 优先用配置的 id_path；未配置或未命中时，回退兼容顶层 id / task_id / taskId——
// 这是各家最常见的三种写法，能让大部分上游"不开配置也能跑通"。
func extractCustomTaskID(root any, idPath string) string {
	if strings.TrimSpace(idPath) != "" {
		if value, ok := lookupDotPath(root, idPath); ok {
			return value
		}
	}
	for _, key := range []string{"id", "task_id", "taskId"} {
		if value, ok := lookupDotPath(root, key); ok {
			return value
		}
	}
	return ""
}

// mapCustomStatus 把上游状态词映射为本地状态。
//
// 次序：先看配置的显式映射（以站长配置为准），未命中再走内置词表。
// 词表识别不了的（含空状态）一律返回"进行中"——保守方向见文件头说明。
func mapCustomStatus(raw string, overrides map[string]model.TaskStatus) model.TaskStatus {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key != "" {
		if status, ok := overrides[key]; ok {
			return status
		}
	}
	status, _ := recognizeCustomStatus(key)
	return status
}

// recognizeCustomStatus 用内置词表识别状态词，返回状态与是否识别成功。
func recognizeCustomStatus(word string) (model.TaskStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "success", "succeeded", "completed", "done", "finished":
		return model.TaskStatusSucceeded, true
	case "failed", "failure", "error":
		return model.TaskStatusFailed, true
	case "canceled", "cancelled":
		return model.TaskStatusCanceled, true
	case "running", "processing", "in_progress", "in-progress":
		return model.TaskStatusRunning, true
	case "queued", "pending", "submitted", "waiting":
		return model.TaskStatusQueued, true
	default:
		return model.TaskStatusRunning, false
	}
}

// parseStatusMap 解析 async_status_map 配置，如 completed=success,running=running。
//
// 容错策略：格式不对或取值无法识别（非三态词）的条目一律忽略——
// 一个写错的条目不该让整份映射失效，更不该把任务导向错误状态。
func parseStatusMap(raw string) map[string]model.TaskStatus {
	result := make(map[string]model.TaskStatus)
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	}) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if status, recognized := recognizeCustomStatus(value); recognized {
			result[key] = status
		}
	}
	return result
}

// sanitizeTaskError 对上游返回的错误文案做脱敏与截断。
//
// 为什么必须脱敏：错误文案可能原样回显请求地址或密钥，
// 而它会落库、经接口返回给客户端，一旦泄漏等于把渠道凭据暴露给所有用户。
func sanitizeTaskError(text string, ch *model.Channel, apiKey string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if ch != nil {
		if base := strings.TrimSpace(ch.BaseURL); len(base) >= 8 {
			text = strings.ReplaceAll(text, base, "上游")
		}
	}
	// 仅当密钥足够长时才替换：避免短字符串（如 "1"）把正常文案打得面目全非。
	if key := strings.TrimSpace(apiKey); len(key) >= 4 {
		text = strings.ReplaceAll(text, key, "[已隐藏]")
	}
	return truncateBytes(text, taskErrorMaxBytes)
}
