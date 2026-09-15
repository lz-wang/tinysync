// Package e2e 承载跨层端到端测试：真实 WebDAV 服务端（x/net/webdav
// httptest）+ 真实 SQLite（storage.Open/Migrate）+ 临时本地目录，
// 验证 v0.3 WebDAV Pull 同步的完整链路与跨重启持久化。
// 这是 v0.3 的 DoD 证据，不依赖任何 mock。
package e2e
