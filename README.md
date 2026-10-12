# cf-yxip

Cloudflare 优选 IP 测试工具 + LuCI 插件，面向 OpenWrt / iStoreOS（aarch64）。

## 组成

- `core/`：Go 后端 `cf-yxipd`，本机 HTTP API（默认 127.0.0.1:7790），负责采集 IP 段、测速打分、结果落盘与下发。
- `luci-app-cf-yxip/`：LuCI 插件（前端页面 + init.d 服务 + uci 配置 + 菜单/ACL），前端通过 `/usr/libexec/cf-yxip` 壳脚本调用后端。
- `build.sh`：交叉编译 aarch64 静态二进制并打出可安装的 tar.gz。

## 构建

    ./build.sh          # 产物在 dist/cf-yxip-<版本>.tar.gz

## 安装（在路由器上执行）

    tar xzf cf-yxip-0.1.0.tar.gz -C /
    /etc/init.d/cf-yxip enable && /etc/init.d/cf-yxip start

安装后 LuCI 菜单：服务 → CF 优选IP。

## 说明

LuCI 静态资源路径为 `/www/luci-static/resources/view/`。
