# Lite Login Extension

轻量插件：从 `apps/go_team_api` 拉取账号并自动执行登录页面填表（同标签页）。

## 功能

- 拉取账号池：`GET /v1/accounts`
- 拉取账号凭据：`GET /v1/accounts/{id}/credentials`
- 自动取件 OTP：`POST /v1/accounts/{id}/otp-fetch`
- 自动登录：
  - 打开 `https://auth.openai.com/log-in`
  - 自动点击/填入邮箱和密码
  - OTP 页面自动取件并自动填充
  - 若登录后回跳 `https://auth.openai.com/log-in`，同 tab 自动打开 `https://chatgpt.com/`
  - hCaptcha 仍保留人工处理
- 订阅成功检测：
  - 监听 `https://chatgpt.com/payments/success-team?...`
  - 不自动回写后端；仅暂存脏数据（待人工处理）
  - 自动提取 access token 并同步到插件面板

## 使用

1. 启动 `apps/go_team_api`（默认 `http://127.0.0.1:18081`）。
2. 在 Chrome 扩展页加载 `extensions/lite_login_extension` 为“已解压扩展程序”。
3. 在插件中配置：
   - `Go API Base`
   - `API Key`（建议 `operator` 或 `admin`）
4. 点击“加载账号”，选择账号后点击“自动登录（同标签页）”。
