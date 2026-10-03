#!/bin/sh
# AQUA-API sky 分支统一推送脚本
# 用途: 绕开 WorkBuddy 环境 helper-selector 在 store 环节打开编辑器导致的无值守挂起
# 方案: GIT_CONFIG_NOSYSTEM=1 跳过 system 级 helper-selector + 显式用 store helper 读 ~/.git-credentials
cd "$(dirname "$0")" || exit 1
export GIT_CONFIG_NOSYSTEM=1
exec git -c credential.helper=store push origin sky "$@"
