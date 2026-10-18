"""在空目录中检查单个发布文件能否独立提供网页和 API。"""

import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def check_release(binary, version):
    environment = {key: value for key, value in os.environ.items() if not key.startswith("OCS_")}
    with tempfile.TemporaryDirectory(prefix="ocs-release-check-") as directory:
        runtime = Path(directory)
        executable = runtime / binary.name
        shutil.copy2(binary, executable)
        executable.chmod(executable.stat().st_mode | 0o100)
        result = subprocess.run(
            [str(executable), "-version"], cwd=runtime, env=environment,
            capture_output=True, text=True, check=True, timeout=10,
        )
        assert result.stdout.strip() == f"OCS-Tiku {version}", result.stdout
        assert not result.stderr.strip(), result.stderr
        assert list(runtime.iterdir()) == [executable], "查询版本不应创建运行文件"

        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        # 本地检查直接连接回环地址，避免维护环境代理影响结果。
        client = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def fetch(route):
            with client.open(base + route, timeout=5) as response:
                assert response.status == 200, (route, response.status)
                return response.read()

        with (runtime / "startup.log").open("w+") as log:
            process = subprocess.Popen(
                [str(executable), "-listen", f"127.0.0.1:{port}"],
                cwd=runtime, env=environment, stdout=log, stderr=log,
            )
            try:
                for _ in range(100):
                    if process.poll() is not None:
                        log.seek(0)
                        raise AssertionError(f"服务启动失败：{log.read()}")
                    try:
                        index = fetch("/")
                        break
                    except (urllib.error.URLError, TimeoutError):
                        time.sleep(0.1)
                else:
                    raise AssertionError("服务启动超时")

                assert b'<div id="root">' in index, "首页内容无效"
                assets = re.findall(r'(?:src|href)="(/assets/[^\"]+)"', index.decode())
                assert any(asset.endswith(".js") for asset in assets), "首页缺少脚本"
                assert any(asset.endswith(".css") for asset in assets), "首页缺少样式"
                for asset in assets:
                    assert fetch(asset), f"资源为空：{asset}"
                for route in ("/records", "/settings"):
                    assert fetch(route) == index, f"页面路由异常：{route}"
                dashboard = json.loads(fetch("/api/dashboard"))
                assert dashboard["stats"]["total"] == 0, dashboard
                assert json.loads(fetch("/api/config")), "题库配置为空"
                assert (runtime / "config.yaml").is_file(), "未生成配置文件"
                assert (runtime / "ocs-tiku.db").is_file(), "未生成数据库"
                assert not (runtime / "frontend").exists(), "运行不应依赖外部网页目录"
                for route in ("/assets/missing.js", "/api/missing"):
                    try:
                        fetch(route)
                    except urllib.error.HTTPError as error:
                        assert error.code == 404, (route, error.code)
                    else:
                        raise AssertionError(f"缺失资源未返回 404：{route}")
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
        print(f"单文件启动检查通过：{binary.name}")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("用法: python3 scripts/check-release.py 可执行文件 版本号")
    check_release(Path(sys.argv[1]).resolve(strict=True), sys.argv[2])
