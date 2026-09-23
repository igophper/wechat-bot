# Contributing / 贡献说明

欢迎中文或英文 Issue 和 Pull Request。请描述实际行为、预期行为、Go/SDK 版本和最小复现；协议问题可附脱敏后的请求或响应，切勿上传登录凭据、context token、验证码或聊天内容。安全问题请按 [SECURITY.md](SECURITY.md) 私密反馈。

Chinese and English issues and pull requests are welcome. Include actual and expected behavior, Go/SDK versions, and a minimal reproduction. Sanitize protocol samples and never include credentials, context tokens, verification codes, or private conversations. Follow [SECURITY.md](SECURITY.md) for security reports.

## Local checks

Use Go 1.26 or 1.27 with its latest patch. Before opening a PR:

```bash
gofmt -w .
go mod tidy -diff
go vet ./...
go test -race -covermode=atomic "-coverprofile=coverage.out" ./...
go build ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2 run ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
git diff --check
```

Install or run the linter with Go 1.27, matching CI. Add focused regression tests for behavior fixes. Tests must use local servers or mocks and must not require a live WeChat account. Update both READMEs and the changelog for public API or user-facing behavior changes. Keep dependencies limited and explain why new ones are needed.

修复功能问题时请补回归测试；测试使用本地服务或 mock，不依赖真实微信账号。公开 API 和用户可见行为变更应同步中英文 README 与更新记录。新增依赖请说明原因。

## Maintainer release process

1. Review the working tree and staged files for runtime state and secrets. Ensure `git check-ignore state/credentials.json` succeeds. If secrets were previously committed, removing them from the current tree is insufficient: rotate them and clean history before publishing.
2. Update the changelog with the version and date, and update the READMEs' release status. Review API compatibility. Use `v0.x.y` while the API evolves; a future `v1` promises backward compatibility for its major version.
3. Run local checks, push the reviewed commit, and wait for CI. Confirm an account login, text reply, and image exchange manually if protocol behavior changed; never put account secrets into CI.
4. Create and push an annotated tag for that exact commit. The following commands publish the first release when the repository is ready:

   ```bash
   git tag -a v0.1.0 -m "Release v0.1.0"
   git push origin v0.1.0
   ```

5. The tag triggers the Release workflow, which repeats CI before creating a GitHub Release. Review the generated notes and add the relevant changelog details. Confirm installation with `go get github.com/igophper/wechat-bot@v0.1.0` in a separate module. Never move a published version tag.

CI uses read-only repository permissions. Only the job that creates a Release has `contents: write`. Actions are pinned to commit hashes, and Dependabot proposes Go dependency and Actions updates. Enable private vulnerability reporting in the GitHub repository settings before public launch; consider protecting the default branch with required CI checks.
