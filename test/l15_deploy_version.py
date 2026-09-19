#!/usr/bin/env python3
"""L15 R16 部署版本可自证性守卫（issue #88）。

背景
====
本地部署的前端镜像曾**落后于仓库 HEAD**，而用户与验收者**都无法察觉**：
容器停在 `compose-web-user:latest`（compose 工程名从 `compose` 改成 `llm` 之后留下的旧名字），
构建时间 2026-09-19T04:51:42Z，早于 #79（阶段路由可达性修复）。
后果是五个阶段路由在已部署系统上全部被旧的重定向逻辑吃掉，被误当成产品缺陷。

父代理复核确认（本脚本的断言 0）：
  - 该镜像 ID 就是 issue 里贴的 1413d7599bdf，标签 `compose-web-user:latest`；
  - 当前运行的 `llm-web-user:latest`（09:40:11Z）已含 #79 的修复；
  - 但「镜像不带版本信息」这个**结构性缺口**仍在 —— 于是补上版本注入与自检。

本脚本锁定的不变量
==================
  A. 构建期版本注入链路存在且完整：Dockerfile 有 ARG/ENV、vite.config 有 define 与
     version.json 产出、compose 有 build.args 透传；
  B. 未注入时**不伪造**版本：表现为 `unknown` 而不是某个看起来像 commit 的值；
  C. 注入后版本号跟着产物走，且 `version.json` 与 JS 里的版本一致（不是两处漂移）；
  D. 自检脚本存在、可执行、且在「取不到 version.json」与「版本不一致」时**非零退出**
     （否则等于没有自检）；
  E. 界面上能看到版本（否则用户/验收者仍然察觉不到）。

分层（CI 可执行性是硬要求）
==========================
CI 的 Frontend job 只跑 `tsc + vite build`，Backend job 只跑 `go test`，**都不起容器**。
因此：
  - 默认路径：只做**静态**断言（读源码/配置/dockerfile），无网络、无容器，由 exit code 决定成败；
  - `--with-api`：额外对**真实部署**断言（version.json 可达、自检脚本判定正确）。
    未启用时输出 [SKIP] 且**不影响 exit code**。

用法
====
  python3 test/l15_deploy_version.py                                   # 静态（CI）
  python3 test/l15_deploy_version.py --with-api --base http://127.0.0.1:3210
"""

import argparse
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))

PASS, FAIL, SKIP = [], [], []


def record(name, ok, detail=""):
    (PASS if ok else FAIL).append(name)
    print(f"  {'PASS' if ok else 'FAIL'}  {name} :: {detail}")


def record_skip(name, detail=""):
    SKIP.append(name)
    print(f"  SKIP  {name} :: {detail}")


def read(rel):
    path = os.path.join(REPO_ROOT, rel)
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as handle:
        return handle.read()


def compose_web_user_build_args(compose):
    """提取 services.web-user.build.args 块。

    刻意不依赖固定字符窗口：注释块的长度会变，用「缩进驱动的块提取」才稳。
    """
    lines = compose.split("\n")
    in_service = indented_args = False
    block = []
    for line in lines:
        stripped = line.strip()
        if re.match(r"^  web-user:\s*$", line):
            in_service = True
            continue
        # 离开 web-user 服务（下一个顶格 2 空格的服务名）
        if in_service and re.match(r"^  \S", line) and not line.startswith("    "):
            break
        if not in_service:
            continue
        if re.match(r"^\s+args:\s*$", line):
            indented_args = True
            continue
        if indented_args:
            if stripped == "" or stripped.startswith("#"):
                continue
            # args 块由更深的缩进构成；一旦回到 args 同级或更浅，块结束
            if re.match(r"^\s*(ports|depends_on|environment|volumes|image|build):", line):
                break
            block.append(stripped)
    return "\n".join(block) if block else None


# ---------------------------------------------------------------------------
# A. 构建期版本注入链路
# ---------------------------------------------------------------------------

def check_injection_chain():
    print("\n[A] 构建期版本注入链路")

    dockerfile = read("deployments/docker/web-user.Dockerfile")
    record("web-user.Dockerfile 存在", dockerfile is not None, "deployments/docker/web-user.Dockerfile")
    if dockerfile is None:
        return
    record("Dockerfile 声明 GIT_SHA 构建参数",
           re.search(r"^ARG\s+GIT_SHA", dockerfile, re.M) is not None,
           "ARG GIT_SHA")
    record("Dockerfile 把 GIT_SHA 传给构建环境（ENV）",
           re.search(r"^ENV\s+GIT_SHA=", dockerfile, re.M) is not None,
           "ENV GIT_SHA=${GIT_SHA}")
    record("Dockerfile 未把默认值写成一个具体 SHA（必须能自证「未注入」）",
           re.search(r"^ARG\s+GIT_SHA=unknown", dockerfile, re.M) is not None,
           "ARG GIT_SHA=unknown")

    vite = read("apps/web-user/vite.config.ts")
    record("vite.config.ts 存在", vite is not None, "apps/web-user/vite.config.ts")
    if vite is None:
        return
    record("vite.config 读取 GIT_SHA 环境变量",
           "process.env.GIT_SHA" in vite, "process.env.GIT_SHA")
    record("vite.config 通过 define 注入 import.meta.env.VITE_APP_VERSION",
           "import.meta.env.VITE_APP_VERSION" in vite and "define" in vite,
           "define['import.meta.env.VITE_APP_VERSION']")
    record("vite.config 产出 version.json（供 curl 5 秒自检）",
           re.search(r"fileName:\s*'version\.json'", vite) is not None
           and "emitFile" in vite,
           "generateBundle -> emitFile(fileName: 'version.json')")

    compose = read("deployments/compose/docker-compose.yml")
    record("compose 把 GIT_SHA 透传为 web-user 的 build.args",
           compose is not None and compose_web_user_build_args(compose) is not None,
           "services.web-user.build.args.GIT_SHA")

    # 默认值必须是 unknown，不能是某个看起来像 commit 的字符串。
    if compose:
        args_block = compose_web_user_build_args(compose)
        match = re.search(r"GIT_SHA:\s*\$\{GIT_SHA:-([^}]*)\}", args_block or "")
        record("compose 中 GIT_SHA 的默认值是 unknown（不是伪造的 SHA）",
               match is not None and match.group(1).strip() == "unknown",
               f"默认值 = {match.group(1)!r}" if match else "未找到 ${GIT_SHA:-...}")

    build_info = read("apps/web-user/src/buildInfo.ts")
    record("buildInfo.ts 存在（版本读取收敛到一处）", build_info is not None,
           "apps/web-user/src/buildInfo.ts")
    if build_info:
        record("buildInfo 未注入时回落到 'unknown'",
               "'unknown'" in build_info, "APP_VERSION = ... || 'unknown'")
        record("buildInfo 明确区分「未注入」状态（供界面提示）",
               "APP_VERSION_UNKNOWN" in build_info, "export const APP_VERSION_UNKNOWN")


# ---------------------------------------------------------------------------
# B/C. 构建产物：未注入不伪造 + 注入后跟产物走
# ---------------------------------------------------------------------------

def check_build_output():
    print("\n[B/C] 构建产物")

    dist = os.path.join(REPO_ROOT, "apps", "web-user", "dist")
    version_json = os.path.join(dist, "version.json")
    if not os.path.exists(version_json):
        record_skip("dist/version.json 存在", "尚未构建（先跑 npm run build -w apps/web-user）")
        return
    with open(version_json, encoding="utf-8") as handle:
        data = json.load(handle)
    record("version.json 是合法 JSON 且含 version / buildTime",
           "version" in data and "buildTime" in data, json.dumps(data, ensure_ascii=False))

    # 未注入构建：必须是 unknown，不能是伪造值。
    # 已注入构建（CI 里通常不注入）：必须是 40/64 位 hex。
    version = str(data.get("version", ""))
    if version == "unknown":
        record("未注入构建时 version='unknown'（不伪造版本号）", True, "version=unknown")
    elif re.fullmatch(r"[0-9a-f]{7,64}", version):
        record("已注入构建时 version 是十六进制 commit", True, f"version={version[:7]}")
    else:
        record("version 只能是 'unknown' 或十六进制 commit",
               False, f"实际 version={version!r}")

    # C：JS 里的版本必须与 version.json 一致（防止两处漂移）。
    assets_dir = os.path.join(dist, "assets")
    if os.path.isdir(assets_dir):
        entry = None
        for name in sorted(os.listdir(assets_dir)):
            if name.startswith("index-") and name.endswith(".js"):
                entry = os.path.join(assets_dir, name)
                break
        if entry:
            with open(entry, encoding="utf-8", errors="replace") as handle:
                js = handle.read()
            if version == "unknown":
                record("入口 JS 内含 'unknown' 版本标记（与 version.json 一致）",
                       "unknown" in js, "index-*.js 含 unknown")
            else:
                record("入口 JS 内含与 version.json 相同的版本号（无两处漂移）",
                       version in js, f"在 index-*.js 中查找 {version[:7]}")


# ---------------------------------------------------------------------------
# D. 自检脚本本身必须「会失败」
# ---------------------------------------------------------------------------

def check_selfcheck_script():
    print("\n[D] 部署自检脚本")

    path = os.path.join(REPO_ROOT, "scripts", "check-deployed-version.sh")
    record("自检脚本存在", os.path.exists(path), "scripts/check-deployed-version.sh")
    if not os.path.exists(path):
        return
    record("自检脚本可执行", os.access(path, os.X_OK), "chmod +x")
    with open(path, encoding="utf-8") as handle:
        script = handle.read()

    # 一个「永远返回 0」的自检等于没有自检 —— 这是本 lane 最需要防的失败模式。
    record("自检脚本在「取不到 version.json」时非零退出",
           "exit 2" in script and "version.json" in script, "exit 2")
    record("自检脚本在「版本不一致」时非零退出",
           "exit 1" in script, "exit 1")
    record("自检脚本会比对本地 HEAD",
           "git rev-parse HEAD" in script, "git rev-parse HEAD")
    record("自检脚本区分「未注入」与「不一致」两种结论",
           "未注入" in script and "不一致" in script, "两条不同的提示路径")

    builder = os.path.join(REPO_ROOT, "scripts", "build-with-version.sh")
    record("提供带版本重建的包装脚本（避免手打环境变量出错）",
           os.path.exists(builder) and os.access(builder, os.X_OK),
           "scripts/build-with-version.sh")


# ---------------------------------------------------------------------------
# E. 界面可见性
# ---------------------------------------------------------------------------

def check_ui_surface():
    print("\n[E] 界面可见性")

    app = read("apps/web-user/src/App.tsx")
    record("App.tsx 存在", app is not None, "apps/web-user/src/App.tsx")
    if app is None:
        return
    record("App.tsx 引入了 buildInfo（版本展示接的是同一份来源）",
           "from './buildInfo'" in app, "import { ... } from './buildInfo'")
    record("帮助页展示「构建版本」卡片", "构建版本" in app, "Title: 构建版本")
    record("界面上直接给出自检命令（用户不用翻文档）",
           "check-deployed-version.sh" in app, "Text code: check-deployed-version.sh")
    record("未注入版本时界面上明确提示「无法自证」",
           "APP_VERSION_UNKNOWN" in app, "条件渲染「未注入」")


# ---------------------------------------------------------------------------
# 可选：对真实部署断言
# ---------------------------------------------------------------------------

def check_live(base):
    print(f"\n[!] 真实部署断言 @ {base}")

    url = f"{base.rstrip('/')}/version.json"
    try:
        with urllib.request.urlopen(url, timeout=8) as res:
            raw = res.read().decode("utf-8")
            status = res.status
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        record(f"部署可达 {url}", False, f"输入缺失/环境不可达：{exc}")
        return

    record(f"部署可达 {url}", status == 200, f"HTTP {status}")
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        record("version.json 可解析", False, str(exc))
        return
    record("version.json 可解析且含 version 字段",
           "version" in data, json.dumps(data, ensure_ascii=False))

    # 真实比对本机 HEAD：这正是 issue #88 想消除的那种误判。
    try:
        head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=REPO_ROOT,
                              capture_output=True, text=True, errors="replace",
                              check=True).stdout.strip()
    except (subprocess.CalledProcessError, FileNotFoundError):
        record_skip("与本地 HEAD 比对", "无法读取本地 git HEAD")
        return

    deployed = str(data.get("version", ""))
    if deployed == "unknown":
        record("部署中的前端能自证版本", False,
               "version=unknown —— 无法判定是否落后于本地 HEAD"
               f"（本地 {head[:7]}）。请用 scripts/build-with-version.sh 重建")
    else:
        record("部署中的前端版本与本地 HEAD 一致",
               deployed == head, f"部署={deployed[:7]} 本地={head[:7]}")

    # 自检脚本必须对当前部署给出正确结论（而不是永远 exit 0）。
    script = os.path.join(REPO_ROOT, "scripts", "check-deployed-version.sh")
    if os.path.exists(script):
        # text=True 会用 locale 默认编码解码，而脚本输出含中文；
        # 显式 errors='replace' 避免在截断的多字节字符上抛 UnicodeDecodeError。
        proc = subprocess.run(["bash", script, base], capture_output=True,
                              text=True, errors="replace")
        expected = 0 if (deployed != "unknown" and deployed == head) else 1
        tail_lines = proc.stdout.strip().splitlines()[-1:]
        record("自检脚本对当前部署给出与事实一致的结论",
               proc.returncode == expected,
               f"exit={proc.returncode}（期望 {expected}）；输出片段="
               + " | ".join(tail_lines or ["(空)"]))


def main():
    parser = argparse.ArgumentParser(description="R16 部署版本可自证性守卫（issue #88）")
    parser.add_argument("--with-api", action="store_true",
                        help="额外对真实部署断言（需前端容器运行）")
    parser.add_argument("--base", default="http://127.0.0.1:3210",
                        help="部署入口地址（配合 --with-api）")
    args = parser.parse_args()

    print("=== L15 R16 部署版本可自证性守卫（issue #88）===")
    check_injection_chain()
    check_build_output()
    check_selfcheck_script()
    check_ui_surface()

    if args.with_api:
        check_live(args.base)
    else:
        record_skip("真实部署断言（version.json 可达 + 与 HEAD 比对 + 自检脚本结论）",
                    "未启用 --with-api（默认路径不需要容器；加 --with-api --base http://127.0.0.1:3210）")

    total = len(PASS) + len(FAIL)
    print(f"\n=== 合计 {total} 项：通过 {len(PASS)}，失败 {len(FAIL)}，跳过 {len(SKIP)} ===")
    if FAIL:
        print("失败项：" + "; ".join(FAIL))
        return 1
    print("全部通过：部署版本可自证性守卫（含「未注入不伪造」与「自检脚本会失败」）。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
