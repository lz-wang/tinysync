import FolderOpenOutlinedIcon from '@mui/icons-material/FolderOpenOutlined'
import KeyOutlinedIcon from '@mui/icons-material/KeyOutlined'
import {
    Alert,
    Box,
    Button,
    Chip,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControl,
    FormControlLabel,
    InputLabel,
    MenuItem,
    Select,
    Stack,
    Switch,
    TextField,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import {
    type CredentialResponse,
    createSource,
    type GitHubReleaseConfig,
    listCredentials,
    type S3Config,
    type SFTPConfig,
    type SFTPCredentials,
    type SourceConfig,
    type SourceCredentials,
    type SourceResponse,
    type SourceType,
    type UpdateSourceInput,
    updateSource,
    type WebDAVConfig,
} from '../../api'
import RemotePathPicker from '../files/RemotePathPicker'
import GitHubReleaseFields from './GitHubReleaseFields'
import PromoteCredentialDialog from './PromoteCredentialDialog'
import SecretField from './SecretField'

interface SourceDialogProps {
    open: boolean
    // 编辑时传入现有 Source；创建时为 null。
    source: SourceResponse | null
    onClose: () => void
    onSaved: (source: SourceResponse) => void
}

// secretKey 标识一个可三态更新的 secret 字段。
type SecretKey =
    | 'webdav.password'
    | 's3.secret_key'
    | 'sftp.password'
    | 'sftp.private_key'
    | 'sftp.passphrase'
    | 'github.token'

// SourceDialog 创建 / 编辑 Source。创建时选择协议类型并按类型动态
// 表单；编辑时 Type readonly（协议不支持原地转换）。secret 绝不回填：
// 保持空白且未修改则不发送；输入新值即替换；Clear 明确清除（空串）。
export default function SourceDialog({ open, source, onClose, onSaved }: SourceDialogProps) {
    const [name, setName] = useState('')
    const [type, setType] = useState<SourceType>('webdav')
    const [enabled, setEnabled] = useState(true)
    const [saving, setSaving] = useState(false)
    const [error, setError] = useState<string | null>(null)
    const [remotePickerOpen, setRemotePickerOpen] = useState(false)

    // 各协议非敏感配置字段。
    const [webdav, setWebdav] = useState<WebDAVConfig>({
        endpoint: '',
        remote_root: '/',
        username: '',
    })
    const [s3, setS3] = useState<S3Config>({
        endpoint: '',
        region: '',
        bucket: '',
        prefix: '',
        path_style: true,
        access_key: '',
    })
    const [sftp, setSftp] = useState<SFTPConfig>({
        host: '',
        port: 22,
        username: '',
        remote_root: '',
        auth_method: 'password',
        host_key_fingerprint: '',
        credential_id: '',
    })
    // keySource 决定 private_key 方式的私钥来源：凭据库引用（与内联
    // 互斥）或一次性粘贴。credentials 是凭据库可选项。
    const [keySource, setKeySource] = useState<'credential' | 'inline'>('inline')
    const [credentials, setCredentials] = useState<CredentialResponse[]>([])
    const [promoteOpen, setPromoteOpen] = useState(false)
    const [github, setGithub] = useState<GitHubReleaseConfig>({
        repository: '',
        release_policy: 'latest',
        tag: '',
        recent_count: 0,
        include_prereleases: false,
        verify_sha256: 'if_available',
    })

    // secret 输入与三态标记：dirty 表示实际编辑，cleared 表示显式清除。
    const [secrets, setSecrets] = useState<Record<SecretKey, string>>({
        'webdav.password': '',
        's3.secret_key': '',
        'sftp.password': '',
        'sftp.private_key': '',
        'sftp.passphrase': '',
        'github.token': '',
    })
    const [secretsDirty, setSecretsDirty] = useState<Set<SecretKey>>(new Set())
    const [secretsCleared, setSecretsCleared] = useState<Set<SecretKey>>(new Set())

    useEffect(() => {
        if (!open) {
            return
        }
        setName(source?.name ?? '')
        setType(source?.type ?? 'webdav')
        setEnabled(source?.enabled ?? true)
        setWebdav({
            endpoint:
                source?.type === 'webdav' ? ((source.config as WebDAVConfig).endpoint ?? '') : '',
            remote_root:
                source?.type === 'webdav'
                    ? ((source.config as WebDAVConfig).remote_root ?? '/')
                    : '/',
            username:
                source?.type === 'webdav' ? ((source.config as WebDAVConfig).username ?? '') : '',
        })
        setS3(
            source?.type === 's3'
                ? (() => {
                      const cfg = source.config as S3Config
                      return {
                          endpoint: cfg.endpoint ?? '',
                          region: cfg.region ?? '',
                          bucket: cfg.bucket ?? '',
                          prefix: cfg.prefix ?? '',
                          path_style: cfg.path_style ?? true,
                          access_key: cfg.access_key ?? '',
                      }
                  })()
                : {
                      endpoint: '',
                      region: '',
                      bucket: '',
                      prefix: '',
                      path_style: true,
                      access_key: '',
                  },
        )
        setSftp(
            source?.type === 'sftp'
                ? (() => {
                      const cfg = source.config as SFTPConfig
                      return {
                          host: cfg.host ?? '',
                          port: cfg.port ?? 22,
                          username: cfg.username ?? '',
                          remote_root: cfg.remote_root ?? '',
                          auth_method: cfg.auth_method ?? 'password',
                          host_key_fingerprint: cfg.host_key_fingerprint ?? '',
                          credential_id: cfg.credential_id ?? '',
                      }
                  })()
                : {
                      host: '',
                      port: 22,
                      username: '',
                      remote_root: '',
                      auth_method: 'password',
                      host_key_fingerprint: '',
                      credential_id: '',
                  },
        )
        // 私钥来源按现有引用态初始化；凭据列表在打开时加载。
        setKeySource(
            source?.type === 'sftp' && (source.config as SFTPConfig).credential_id
                ? 'credential'
                : 'inline',
        )
        setGithub(
            source?.type === 'github_release'
                ? (() => {
                      const cfg = source.config as GitHubReleaseConfig
                      return {
                          repository: cfg.repository ?? '',
                          release_policy: cfg.release_policy ?? 'latest',
                          tag: cfg.tag ?? '',
                          recent_count: cfg.recent_count ?? 0,
                          include_prereleases: cfg.include_prereleases ?? false,
                          verify_sha256: cfg.verify_sha256 ?? 'if_available',
                      }
                  })()
                : {
                      repository: '',
                      release_policy: 'latest',
                      tag: '',
                      recent_count: 0,
                      include_prereleases: false,
                      verify_sha256: 'if_available',
                  },
        )
        setSecrets({
            'webdav.password': '',
            's3.secret_key': '',
            'sftp.password': '',
            'sftp.private_key': '',
            'sftp.passphrase': '',
            'github.token': '',
        })
        setSecretsDirty(new Set())
        setSecretsCleared(new Set())
        setSaving(false)
        setError(null)
    }, [open, source])

    // 凭据库选项：SFTP 表单打开时加载；失败按空列表降级（选择器内
    // 提示先建凭据）。
    useEffect(() => {
        if (!open || type !== 'sftp') {
            return
        }
        let cancelled = false
        listCredentials()
            .then(list => {
                if (!cancelled) {
                    setCredentials(list)
                }
            })
            .catch(() => {
                if (!cancelled) {
                    setCredentials([])
                }
            })
        return () => {
            cancelled = true
        }
    }, [open, type])

    function setSecret(key: SecretKey, value: string) {
        setSecrets(prev => ({ ...prev, [key]: value }))
        setSecretsDirty(prev => new Set(prev).add(key))
        setSecretsCleared(prev => {
            const next = new Set(prev)
            next.delete(key)
            return next
        })
    }

    function clearSecret(key: SecretKey) {
        setSecrets(prev => ({ ...prev, [key]: '' }))
        setSecretsDirty(prev => new Set(prev).add(key))
        setSecretsCleared(prev => new Set(prev).add(key))
    }

    function secretState(key: SecretKey): { configured: boolean; label: string | null } {
        if (source === null) {
            return { configured: false, label: null }
        }
        const configured = isSecretConfigured(source, key)
        if (secretsCleared.has(key)) {
            return { configured, label: '保存后将清除' }
        }
        return { configured, label: configured ? '已配置' : '未设置' }
    }

    function buildConfig(): SourceConfig {
        if (type === 'webdav') {
            return { ...webdav }
        }
        if (type === 's3') {
            return { ...s3 }
        }
        if (type === 'github_release') {
            return { ...github }
        }
        // 内联一次性私钥时 credential_id 恒为空：引用与内联互斥。
        return keySource === 'credential' ? { ...sftp } : { ...sftp, credential_id: '' }
    }

    function buildCredentials(): SourceCredentials | undefined {
        if (type === 'webdav') {
            if (secretsDirty.has('webdav.password')) {
                return { password: secrets['webdav.password'] }
            }
            return undefined
        }
        if (type === 's3') {
            if (secretsDirty.has('s3.secret_key')) {
                return { secret_key: secrets['s3.secret_key'] }
            }
            return undefined
        }
        if (type === 'github_release') {
            if (secretsDirty.has('github.token')) {
                return { token: secrets['github.token'] }
            }
            return undefined
        }
        if (keySource === 'credential') {
            // 引用与内联互斥：凭据库模式下残留的 dirty 内联输入一律丢弃。
            return undefined
        }
        const creds: SFTPCredentials = {}
        let changed = false
        for (const key of ['sftp.password', 'sftp.private_key', 'sftp.passphrase'] as const) {
            if (!secretsDirty.has(key)) {
                continue
            }
            changed = true
            if (key === 'sftp.password') {
                creds.password = secrets[key]
            } else if (key === 'sftp.private_key') {
                creds.private_key = secrets[key]
            } else {
                creds.private_key_passphrase = secrets[key]
            }
        }
        return changed ? creds : undefined
    }

    async function handleSave() {
        setSaving(true)
        setError(null)
        try {
            if (source === null) {
                const credentials = buildCredentials()
                const created = await createSource({
                    name,
                    type,
                    config: buildConfig(),
                    credentials:
                        credentials !== undefined && Object.keys(credentials).length > 0
                            ? credentials
                            : undefined,
                    enabled,
                })
                onSaved(created)
                return
            }
            const patch: UpdateSourceInput = {}
            if (name !== source.name) {
                patch.name = name
            }
            const nextConfig = buildConfig()
            if (JSON.stringify(nextConfig) !== JSON.stringify(source.config)) {
                patch.config = nextConfig
            }
            const credentials = buildCredentials()
            if (credentials !== undefined) {
                patch.credentials = credentials
            }
            if (enabled !== source.enabled) {
                patch.enabled = enabled
            }
            const updated = await updateSource(source.id, patch)
            onSaved(updated)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setSaving(false)
        }
    }

    function canSave(): boolean {
        if (name.trim() === '') {
            return false
        }
        if (type === 'webdav') {
            return webdav.endpoint.trim() !== ''
        }
        if (type === 's3') {
            return source !== null
                ? s3.endpoint?.trim() !== '' &&
                      s3.bucket.trim() !== '' &&
                      s3.access_key.trim() !== ''
                : s3.endpoint?.trim() !== '' &&
                      s3.bucket.trim() !== '' &&
                      s3.access_key.trim() !== '' &&
                      secrets['s3.secret_key'].trim() !== ''
        }
        if (type === 'github_release') {
            if (github.repository.trim() === '') {
                return false
            }
            if (github.release_policy === 'tag' && (github.tag ?? '').trim() === '') {
                return false
            }
            if (github.release_policy === 'recent') {
                const count = github.recent_count ?? 0
                if (!Number.isInteger(count) || count < 1 || count > 1000) {
                    return false
                }
            }
            return true
        }
        if (sftp.host.trim() === '' || sftp.username.trim() === '') {
            return false
        }
        if (sftp.auth_method === 'private_key') {
            if (keySource === 'credential') {
                return (sftp.credential_id ?? '') !== ''
            }
            // 内联模式必须持有私钥：存量已有（未清除）或本次粘贴——
            // 从引用态切回内联时存量已被清除，必须重贴。
            const hasStored =
                source !== null && (source.credential_state.sftp?.private_key_set ?? false)
            const pasted = secrets['sftp.private_key'].trim() !== ''
            return hasStored || pasted
        }
        return true
    }

    // promoteEligible：编辑态 + 内联私钥已存 + 未引用 → 可一键提升为
    // 凭据（提升后由后端改写为引用态）。
    const promoteEligible =
        source !== null &&
        type === 'sftp' &&
        sftp.auth_method === 'private_key' &&
        keySource === 'inline' &&
        (source.credential_state.sftp?.private_key_set ?? false)

    const secretChip = (key: SecretKey, label: string) => {
        if (source === null) {
            return null
        }
        const state = secretState(key)
        return (
            <Chip
                size="small"
                label={`${label}：${state.label ?? '未设置'}`}
                color={secretsCleared.has(key) ? 'warning' : 'default'}
            />
        )
    }

    return (
        <Dialog
            open={open}
            onClose={onClose}
            scroll="paper"
            disableRestoreFocus
            slotProps={{
                paper: {
                    sx: {
                        width: '60vw',
                        height: '80vh',
                        maxWidth: 'none',
                        maxHeight: 'none',
                    },
                },
            }}
        >
            <DialogTitle>{source === null ? '创建同步源' : '编辑同步源'}</DialogTitle>
            <DialogContent sx={{ flex: 1, overflowY: 'auto' }}>
                <Stack spacing={2} sx={{ pt: 1 }}>
                    {error !== null && <Alert severity="error">{error}</Alert>}
                    <TextField
                        label="名称"
                        value={name}
                        onChange={e => setName(e.target.value)}
                        required
                        autoFocus
                    />
                    <FormControl fullWidth>
                        <InputLabel id="source-type-label">类型</InputLabel>
                        <Select
                            labelId="source-type-label"
                            value={type}
                            label="类型"
                            disabled={source !== null}
                            onChange={e => setType(e.target.value as SourceType)}
                        >
                            <MenuItem value="webdav">WebDAV</MenuItem>
                            <MenuItem value="s3">S3</MenuItem>
                            <MenuItem value="sftp">SFTP</MenuItem>
                            <MenuItem value="github_release">GitHub Release</MenuItem>
                        </Select>
                    </FormControl>
                    {source !== null && (
                        <Typography variant="caption" color="text.secondary">
                            创建后不能修改同步源类型。
                        </Typography>
                    )}

                    {type === 'webdav' && (
                        <>
                            <TextField
                                label="服务地址"
                                value={webdav.endpoint}
                                onChange={e => setWebdav({ ...webdav, endpoint: e.target.value })}
                                required
                                placeholder="https://nas.example.com:5006/dav"
                                sx={{ '& input': { fontFamily: 'monospace' } }}
                            />
                            <RootField
                                value={webdav.remote_root ?? '/'}
                                onChange={value => setWebdav({ ...webdav, remote_root: value })}
                                onBrowse={() => setRemotePickerOpen(true)}
                                disabled={source === null}
                                helperText={
                                    source === null
                                        ? '保存同步源后可浏览选择目录。'
                                        : '留在此目录下同步，任务中的路径相对此根目录。'
                                }
                            />
                            <Box
                                sx={{
                                    display: 'grid',
                                    gridTemplateColumns: 'repeat(2, minmax(0, 1fr))',
                                    gap: 2,
                                }}
                            >
                                <TextField
                                    label="用户名"
                                    value={webdav.username}
                                    onChange={e =>
                                        setWebdav({ ...webdav, username: e.target.value })
                                    }
                                />
                                <SecretField
                                    label="密码"
                                    value={secrets['webdav.password']}
                                    dirty={secretsDirty.has('webdav.password')}
                                    cleared={secretsCleared.has('webdav.password')}
                                    chip={secretChip('webdav.password', '密码')}
                                    onChange={v => setSecret('webdav.password', v)}
                                    onClear={() => clearSecret('webdav.password')}
                                />
                            </Box>
                        </>
                    )}

                    {type === 's3' && (
                        <>
                            <Box
                                sx={{
                                    display: 'grid',
                                    gridTemplateColumns: 'repeat(2, minmax(0, 1fr))',
                                    gap: 2,
                                }}
                            >
                                <TextField
                                    label="服务地址"
                                    value={s3.endpoint ?? ''}
                                    onChange={e => setS3({ ...s3, endpoint: e.target.value })}
                                    placeholder="https://s3.example.com"
                                    required
                                    sx={{ '& input': { fontFamily: 'monospace' } }}
                                />
                                <TextField
                                    label="区域"
                                    value={s3.region}
                                    onChange={e => setS3({ ...s3, region: e.target.value })}
                                    placeholder="us-east-1"
                                />
                            </Box>
                            <Box
                                sx={{
                                    display: 'grid',
                                    gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1fr) auto',
                                    gap: 2,
                                    alignItems: 'center',
                                }}
                            >
                                <TextField
                                    label="存储桶"
                                    value={s3.bucket}
                                    onChange={e => setS3({ ...s3, bucket: e.target.value })}
                                    required
                                    sx={{ '& input': { fontFamily: 'monospace' } }}
                                />
                                <TextField
                                    label="前缀（可选）"
                                    value={s3.prefix ?? ''}
                                    onChange={e => setS3({ ...s3, prefix: e.target.value })}
                                    placeholder="tinysync"
                                    sx={{ '& input': { fontFamily: 'monospace' } }}
                                />
                                <FormControlLabel
                                    control={
                                        <Switch
                                            checked={s3.path_style ?? true}
                                            onChange={e =>
                                                setS3({ ...s3, path_style: e.target.checked })
                                            }
                                        />
                                    }
                                    label="路径风格寻址（MinIO / 自托管）"
                                />
                            </Box>
                            <Box
                                sx={{
                                    display: 'grid',
                                    gridTemplateColumns: 'repeat(2, minmax(0, 1fr))',
                                    gap: 2,
                                }}
                            >
                                <TextField
                                    label="Access Key"
                                    value={s3.access_key}
                                    onChange={e => setS3({ ...s3, access_key: e.target.value })}
                                    required
                                    sx={{ '& input': { fontFamily: 'monospace' } }}
                                />
                                <SecretField
                                    label="Secret Key"
                                    value={secrets['s3.secret_key']}
                                    dirty={secretsDirty.has('s3.secret_key')}
                                    cleared={secretsCleared.has('s3.secret_key')}
                                    chip={secretChip('s3.secret_key', 'Secret Key')}
                                    onChange={v => setSecret('s3.secret_key', v)}
                                    onClear={() => clearSecret('s3.secret_key')}
                                />
                            </Box>
                        </>
                    )}

                    {type === 'github_release' && (
                        <GitHubReleaseFields
                            config={github}
                            onChange={setGithub}
                            token={secrets['github.token']}
                            tokenDirty={secretsDirty.has('github.token')}
                            tokenCleared={secretsCleared.has('github.token')}
                            tokenChip={secretChip('github.token', 'Token')}
                            onTokenChange={v => setSecret('github.token', v)}
                            onTokenClear={() => clearSecret('github.token')}
                            source={source}
                        />
                    )}

                    {type === 'sftp' && (
                        <>
                            <Box
                                sx={{
                                    display: 'grid',
                                    gridTemplateColumns: 'repeat(2, minmax(0, 1fr))',
                                    gap: 2,
                                }}
                            >
                                <TextField
                                    label="主机"
                                    value={sftp.host}
                                    onChange={e => setSftp({ ...sftp, host: e.target.value })}
                                    required
                                    placeholder="nas.example.com"
                                />
                                <TextField
                                    label="端口"
                                    type="number"
                                    value={sftp.port ?? 22}
                                    onChange={e =>
                                        setSftp({ ...sftp, port: Number(e.target.value) })
                                    }
                                />
                            </Box>
                            <Box
                                sx={{
                                    display: 'grid',
                                    gridTemplateColumns: 'repeat(2, minmax(0, 1fr))',
                                    gap: 2,
                                }}
                            >
                                <TextField
                                    label="用户名"
                                    value={sftp.username}
                                    onChange={e => setSftp({ ...sftp, username: e.target.value })}
                                    required
                                />
                                <FormControl fullWidth>
                                    <InputLabel id="sftp-auth-label">认证方式</InputLabel>
                                    <Select
                                        labelId="sftp-auth-label"
                                        value={sftp.auth_method}
                                        label="认证方式"
                                        onChange={e => {
                                            const next = e.target.value as SFTPConfig['auth_method']
                                            if (next === 'password') {
                                                // 密码方式不携带凭据引用：清引用并
                                                // 复位私钥来源，避免过期 credential_id
                                                // 触发后端 400。
                                                setKeySource('inline')
                                                setSftp({
                                                    ...sftp,
                                                    auth_method: next,
                                                    credential_id: '',
                                                })
                                                return
                                            }
                                            setSftp({ ...sftp, auth_method: next })
                                        }}
                                    >
                                        <MenuItem value="password">密码</MenuItem>
                                        <MenuItem value="private_key">私钥</MenuItem>
                                    </Select>
                                </FormControl>
                            </Box>
                            {sftp.auth_method === 'password' ? (
                                <SecretField
                                    label="密码"
                                    value={secrets['sftp.password']}
                                    dirty={secretsDirty.has('sftp.password')}
                                    cleared={secretsCleared.has('sftp.password')}
                                    chip={secretChip('sftp.password', '密码')}
                                    onChange={v => setSecret('sftp.password', v)}
                                    onClear={() => clearSecret('sftp.password')}
                                />
                            ) : (
                                <>
                                    <FormControl fullWidth>
                                        <InputLabel id="sftp-key-source-label">私钥来源</InputLabel>
                                        <Select
                                            labelId="sftp-key-source-label"
                                            value={keySource}
                                            label="私钥来源"
                                            onChange={e => {
                                                const next = e.target.value as
                                                    | 'credential'
                                                    | 'inline'
                                                setKeySource(next)
                                                if (next === 'inline') {
                                                    setSftp({ ...sftp, credential_id: '' })
                                                }
                                            }}
                                        >
                                            <MenuItem value="credential">从凭据库选择</MenuItem>
                                            <MenuItem value="inline">粘贴一次性私钥</MenuItem>
                                        </Select>
                                    </FormControl>
                                    {keySource === 'credential' ? (
                                        <FormControl fullWidth required>
                                            <InputLabel id="sftp-credential-label">凭据</InputLabel>
                                            <Select
                                                labelId="sftp-credential-label"
                                                value={sftp.credential_id ?? ''}
                                                label="凭据"
                                                onChange={e =>
                                                    setSftp({
                                                        ...sftp,
                                                        credential_id: e.target.value,
                                                    })
                                                }
                                            >
                                                {credentials.length === 0 && (
                                                    <MenuItem value="">
                                                        <em>凭据库为空或加载中</em>
                                                    </MenuItem>
                                                )}
                                                {credentials.map(credential => (
                                                    <MenuItem
                                                        key={credential.id}
                                                        value={credential.id}
                                                    >
                                                        {credential.name} · {credential.fingerprint}
                                                        {credential.has_passphrase
                                                            ? '（带口令）'
                                                            : ''}
                                                    </MenuItem>
                                                ))}
                                            </Select>
                                            {credentials.length === 0 ? (
                                                <Typography
                                                    variant="caption"
                                                    color="text.secondary"
                                                    sx={{ mt: 1 }}
                                                >
                                                    凭据库为空：请先在「凭据」页创建 SSH 私钥凭据。
                                                </Typography>
                                            ) : (
                                                <Typography
                                                    variant="caption"
                                                    color="text.secondary"
                                                    sx={{ mt: 1 }}
                                                >
                                                    引用态换钥在「凭据」页一次完成，全部引用源自动生效；引用与内联私钥互斥。
                                                </Typography>
                                            )}
                                        </FormControl>
                                    ) : (
                                        <>
                                            <SecretField
                                                label="私钥（PEM）"
                                                value={secrets['sftp.private_key']}
                                                dirty={secretsDirty.has('sftp.private_key')}
                                                cleared={secretsCleared.has('sftp.private_key')}
                                                chip={secretChip('sftp.private_key', '私钥')}
                                                onChange={v => setSecret('sftp.private_key', v)}
                                                onClear={() => clearSecret('sftp.private_key')}
                                                multiline
                                            />
                                            <SecretField
                                                label="私钥口令（可选）"
                                                value={secrets['sftp.passphrase']}
                                                dirty={secretsDirty.has('sftp.passphrase')}
                                                cleared={secretsCleared.has('sftp.passphrase')}
                                                chip={secretChip('sftp.passphrase', '私钥口令')}
                                                onChange={v => setSecret('sftp.passphrase', v)}
                                                onClear={() => clearSecret('sftp.passphrase')}
                                            />
                                            {promoteEligible && (
                                                <Box>
                                                    <Button
                                                        variant="outlined"
                                                        size="small"
                                                        startIcon={
                                                            <KeyOutlinedIcon fontSize="small" />
                                                        }
                                                        onClick={() => setPromoteOpen(true)}
                                                    >
                                                        提升为凭据
                                                    </Button>
                                                    <Typography
                                                        variant="caption"
                                                        color="text.secondary"
                                                        sx={{ display: 'block', mt: 0.5 }}
                                                    >
                                                        把已保存的私钥转存为命名凭据并改为引用，换钥一次生效全部。
                                                    </Typography>
                                                </Box>
                                            )}
                                        </>
                                    )}
                                </>
                            )}
                            <RootField
                                value={sftp.remote_root ?? ''}
                                onChange={value => setSftp({ ...sftp, remote_root: value })}
                                onBrowse={() => setRemotePickerOpen(true)}
                                disabled={source === null}
                                helperText={
                                    source === null
                                        ? '留空使用该用户 Home；保存同步源后可浏览选择目录。'
                                        : '留空使用该用户 Home；任务中的路径相对此根目录。'
                                }
                            />
                        </>
                    )}

                    {type === 'sftp' && (
                        <TextField
                            label="主机密钥指纹（SHA256，可选）"
                            value={sftp.host_key_fingerprint}
                            onChange={e =>
                                setSftp({ ...sftp, host_key_fingerprint: e.target.value })
                            }
                            placeholder="SHA256:UC1Dk4I9LLQOV3B8eZ5FlrUUcbbNie4INffe2TDTz3k"
                            sx={{ '& input': { fontFamily: 'monospace' } }}
                            helperText="留空将跳过主机密钥校验；填写后会严格校验。"
                        />
                    )}
                </Stack>
            </DialogContent>
            <DialogActions>
                <FormControlLabel
                    control={
                        <Switch
                            checked={enabled}
                            onChange={e => setEnabled(e.target.checked)}
                            disabled={saving}
                        />
                    }
                    label="启用"
                    sx={{ mr: 'auto' }}
                />
                <Button onClick={onClose} disabled={saving}>
                    取消
                </Button>
                <Button
                    onClick={() => void handleSave()}
                    variant="contained"
                    disabled={saving || !canSave()}
                >
                    {saving ? '保存中…' : '保存'}
                </Button>
            </DialogActions>
            <RemotePathPicker
                open={remotePickerOpen}
                onClose={() => setRemotePickerOpen(false)}
                onPick={path => {
                    if (type === 'webdav') {
                        setWebdav({ ...webdav, remote_root: path })
                    } else {
                        setSftp({ ...sftp, remote_root: path })
                    }
                    setRemotePickerOpen(false)
                }}
                boundSourceId={source?.id}
                initialPath={
                    type === 'webdav' ? (webdav.remote_root ?? '/') : sftp.remote_root || '/'
                }
            />
            <PromoteCredentialDialog
                open={promoteOpen}
                source={promoteEligible ? source : null}
                onClose={() => setPromoteOpen(false)}
                onPromoted={updated => {
                    setPromoteOpen(false)
                    onSaved(updated)
                }}
            />
        </Dialog>
    )
}

function RootField({
    value,
    onChange,
    onBrowse,
    disabled,
    helperText,
}: {
    value: string
    onChange: (value: string) => void
    onBrowse: () => void
    disabled: boolean
    helperText: string
}) {
    return (
        <Box sx={{ display: 'flex', gap: 1.5, alignItems: 'flex-start', minWidth: 0 }}>
            <TextField
                label="远端根目录"
                value={value}
                onChange={event => onChange(event.target.value)}
                placeholder="/"
                helperText={helperText}
                sx={{ flex: 1, minWidth: 0, '& input': { fontFamily: 'monospace' } }}
            />
            <Button
                variant="outlined"
                size="large"
                startIcon={<FolderOpenOutlinedIcon />}
                disabled={disabled}
                onClick={onBrowse}
                sx={{ mt: 0.5, minWidth: 104, height: 48, flexShrink: 0 }}
            >
                浏览
            </Button>
        </Box>
    )
}

// isSecretConfigured 从响应 credential_state 读取 secret 是否已设置。
function isSecretConfigured(source: SourceResponse, key: SecretKey): boolean {
    const state = source.credential_state
    switch (key) {
        case 'webdav.password':
            return state.webdav?.password_set ?? false
        case 's3.secret_key':
            return state.s3?.secret_key_set ?? false
        case 'sftp.password':
            return state.sftp?.password_set ?? false
        case 'sftp.private_key':
            return state.sftp?.private_key_set ?? false
        case 'sftp.passphrase':
            return state.sftp?.private_key_passphrase_set ?? false
        case 'github.token':
            return state.github_release?.token_set ?? false
    }
}
