package source

// RemoteIdentityEqual 判断两个 Source 的远端身份是否一致。身份字段
// 由协议决定：
//
//	WebDAV  endpoint + username
//	S3      endpoint + region + bucket + prefix + path_style
//	        （access_key 属凭据组件，可随 secret_key 轮换）
//	SFTP    host + port + username + remote_root + host key fingerprint
//	        （auth_method 切换不改变物理远端身份）
//	GitHub  repository + release_policy + tag + recent_count +
//	        include_prereleases（版本选择范围决定逻辑目录内容，改变
//	        即改变 Mirror 的删除范围；verify_sha256 只影响校验强度，
//	        token 轮换只影响访问，均不属于身份）
//
// 被 Sync Job 引用的 Source 禁止身份变更（409）：身份变化后 Mirror
// 下轮完整扫描可能把既有 managed 文件全部误判为远端消失而删除。
// name / enabled / secret rotation 不属于身份，始终允许修改。
func RemoteIdentityEqual(a, b Source) bool {
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case TypeWebDAV:
		if a.Config.WebDAV == nil || b.Config.WebDAV == nil {
			return a.Config.WebDAV == b.Config.WebDAV
		}
		return *a.Config.WebDAV == *b.Config.WebDAV
	case TypeS3:
		if a.Config.S3 == nil || b.Config.S3 == nil {
			return a.Config.S3 == b.Config.S3
		}
		x, y := *a.Config.S3, *b.Config.S3
		return x.Endpoint == y.Endpoint &&
			x.Region == y.Region &&
			x.Bucket == y.Bucket &&
			x.Prefix == y.Prefix &&
			x.PathStyle == y.PathStyle
	case TypeSFTP:
		if a.Config.SFTP == nil || b.Config.SFTP == nil {
			return a.Config.SFTP == b.Config.SFTP
		}
		x, y := *a.Config.SFTP, *b.Config.SFTP
		return x.Host == y.Host &&
			x.Port == y.Port &&
			x.Username == y.Username &&
			x.RemoteRoot == y.RemoteRoot &&
			x.HostKeyFingerprint == y.HostKeyFingerprint
	case TypeGitHubRelease:
		if a.Config.GitHubRelease == nil || b.Config.GitHubRelease == nil {
			return a.Config.GitHubRelease == b.Config.GitHubRelease
		}
		x, y := *a.Config.GitHubRelease, *b.Config.GitHubRelease
		return x.Repository == y.Repository &&
			x.ReleasePolicy == y.ReleasePolicy &&
			x.Tag == y.Tag &&
			x.RecentCount == y.RecentCount &&
			x.IncludePrereleases == y.IncludePrereleases
	default:
		return false
	}
}
