// 本文件是「动态综合评分算法 v2.3」的可运行示例：
//
//	go run ./internal/score/cmd/example
//
// 用途有三个：
//  1. 一份 <= 30 行的最小用法（新建文件时照抄即可）；
//  2. 用冻结数据集 A/A2 跑出的实际输出表，与规格表格逐值对照；
//  3. 演示"动态性"：把某人的 token 抬高后，全榜分数重新标定。
package main

import (
	"fmt"

	"github.com/LTZY-ACU/ltzy-api/internal/score"
)

func main() {
	// ── 1) 最小用法：给每人一个 ID 与两项用量，拿到带名次的榜单 ──
	entries := []score.Entry{
		{ID: "alice", Tokens: 12000, Requests: 800},
		{ID: "bob", Tokens: 9000, Requests: 500},
		{ID: "carol", Tokens: 6000, Requests: 300},
		{ID: "dave", Tokens: 3000, Requests: 120},
		{ID: "erin", Tokens: 20000, Requests: 60},
		{ID: "frank", Tokens: 400, Requests: 5000},
	}

	show("默认配置（pTop=0.90, pBase=0.10）", entries, score.DefaultConfig)

	// ── 2) 动态性：erin 的 token 从 2 万抬到 10 万，锚点随之上移 ──
	raised := append([]score.Entry(nil), entries...)
	raised[4].Tokens = 100000
	show("erin.token 20,000 → 100,000", raised, score.DefaultConfig)

	// ── 3) 自定义配置：把上锚点锚在最大值（P100）、下锚点取最小值（P0） ──
	show("pTop=1.0, pBase=0.0", entries, score.Config{
		TopPercent: 1.0, BasePct: 0.0, SetBasePct: true,
	})
}

func show(title string, entries []score.Entry, cfg score.Config) {
	res := score.ScoreBoardWithConfig(entries, cfg)
	fmt.Printf("\n=== %s ===\n", title)
	fmt.Printf("锚点: token[%.0f, %.0f]  request[%.0f, %.0f]\n",
		res.Anchors.BaseTok, res.Anchors.TopTok, res.Anchors.BaseReq, res.Anchors.TopReq)
	fmt.Println("名次  账号      请求数      Token     sTok    sReq   总分")
	for _, r := range res.Rows {
		fmt.Printf("%4d  %-8s %8.0f %10.0f  %6.2f %6.2f %6.2f\n",
			r.Rank, r.ID, r.Requests, r.Tokens, r.ScoreTok, r.ScoreReq, r.Total)
	}
}
