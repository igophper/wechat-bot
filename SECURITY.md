# Security policy / 安全问题反馈

## Reporting

Please report suspected vulnerabilities through [GitHub private vulnerability reporting](https://github.com/igophper/wechat-bot/security/advisories/new). Include affected versions, a minimal reproduction, impact, and a proposed fix if available. Do not include real bot tokens, context tokens, QR codes, verification codes, or private messages.

If private reporting is not available yet, open an issue requesting a private contact channel **without disclosing vulnerability details or secrets**. No response-time guarantee is currently offered.

请优先通过 [GitHub 私密漏洞报告](https://github.com/igophper/wechat-bot/security/advisories/new) 反馈，并提供受影响版本、最小复现和影响范围。不要上传真实凭据或聊天内容。如果暂未开启私密报告，请在公开 Issue 中仅请求私密联系方式，不要公开漏洞细节。目前没有承诺固定响应时限。

## Supported versions

Security fixes target the latest released version. Older versions are not guaranteed backports. Use the latest SDK release and the latest patch of a supported Go branch.

安全修复面向最新发行版，不保证向旧版本回移。请使用最新 SDK 发行版及受支持 Go 分支的最新补丁版。

## Handling credentials

Credential and cursor files are local application data, not source code. The file stores use restrictive Unix permissions and replace files atomically where the operating system supports that behavior. On Windows, protect the state directory with appropriate filesystem ACLs; Unix mode bits do not provide equivalent access control there. Do not share one state directory between independently running bot instances.

The protocol uses AES-128-ECB for media and MD5 for upload metadata. Those algorithms are kept for platform interoperability. They do not provide a general-purpose authenticated encryption design. Never use this SDK's protocol helpers to protect unrelated sensitive data.
