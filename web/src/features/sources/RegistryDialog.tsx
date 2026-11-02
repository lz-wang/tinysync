import {
    Alert,
    Button,
    Checkbox,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControlLabel,
    MenuItem,
    Stack,
    Switch,
    TextField,
    Tooltip,
} from '@mui/material'
import { useEffect, useState } from 'react'
import {
    type CreateRegistryInput,
    createRegistry,
    type RegistryKind,
    type RegistryResponse,
    type UpdateRegistryInput,
    updateRegistry,
} from '../../api'

interface RegistryDialogProps {
    open: boolean
    // 编辑时传入现有行；创建时为 null（createKind 决定行类型）。
    registry: RegistryResponse | null
    createKind: RegistryKind
    onClose: () => void
    onSaved: (registry: RegistryResponse) => void
}

interface RegistryFormState {
    kind: RegistryKind
    name: string
    host: string
    username: string
    password: string
    clearPassword: boolean
    insecureSkipVerify: boolean
    plainHTTP: boolean
    stripText: string
    pathTransformEnabled: boolean
    pathSeparator: string
    pathMode: string
    mirrorsText: string
}

function initialState(
    registry: RegistryResponse | null,
    createKind: RegistryKind,
): RegistryFormState {
    if (registry === null) {
        return {
            kind: createKind,
            name: '',
            host: '',
            username: '',
            password: '',
            clearPassword: false,
            insecureSkipVerify: false,
            plainHTTP: false,
            stripText: '',
            pathTransformEnabled: false,
            pathSeparator: '',
            pathMode: 'both',
            mirrorsText: '',
        }
    }
    return {
        kind: registry.kind,
        name: registry.name,
        host: registry.host,
        username: registry.username,
        password: '',
        clearPassword: false,
        insecureSkipVerify: registry.insecure_skip_verify,
        plainHTTP: registry.plain_http,
        stripText: (registry.strip_registries ?? []).join('\n'),
        pathTransformEnabled: registry.path_transform !== null,
        pathSeparator: registry.path_transform?.separator ?? '',
        pathMode: registry.path_transform?.mode ?? 'both',
        mirrorsText: (registry.mirrors ?? []).join(', '),
    }
}

// parseMirrors 把加速地址输入拆为地址数组（逗号分隔、去空白）。
function parseMirrors(text: string): string[] {
    return text
        .split(',')
        .map(value => value.trim())
        .filter(value => value !== '')
}

// parseStripLines 把多行前缀输入拆为数组（一行一个、去空白）。
function parseStripLines(text: string): string[] {
    return text
        .split('\n')
        .map(value => value.trim())
        .filter(value => value !== '')
}

// RegistryDialog 是 Registry 配置的创建 / 编辑对话框（oci-sync
// RegistrySettingsPanel / MirrorSettingsPanel 表单合并移植，存储从
// settings 全量 PUT 改为逐项 CRUD）。校验采用 per-field 错误映射（R34）：
// 名称 / 地址必填，mirror 行至少一个加速地址；password 三态——留空保留、
// 勾选清除、输入替换。
export default function RegistryDialog({
    open,
    registry,
    createKind,
    onClose,
    onSaved,
}: RegistryDialogProps) {
    const [form, setForm] = useState<RegistryFormState>(() => initialState(registry, createKind))
    const [errors, setErrors] = useState<Record<string, string>>({})
    const [saving, setSaving] = useState(false)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        if (!open) {
            return
        }
        setForm(initialState(registry, createKind))
        setErrors({})
        setSaving(false)
        setError(null)
    }, [open, registry, createKind])

    const setField = <K extends keyof RegistryFormState>(key: K, value: RegistryFormState[K]) => {
        setForm(prev => ({ ...prev, [key]: value }))
        setErrors(prev => {
            if (prev[key] === undefined) {
                return prev
            }
            const next = { ...prev }
            delete next[key]
            return next
        })
    }

    const validate = (): boolean => {
        const next: Record<string, string> = {}
        if (form.name.trim() === '') {
            next.name = '名称不能为空'
        }
        if (form.host.trim() === '') {
            next.host =
                form.kind === 'mirror' ? '源 Registry 主机不能为空' : 'Registry 地址不能为空'
        }
        if (form.kind === 'mirror' && parseMirrors(form.mirrorsText).length === 0) {
            next.mirrorsText = '至少填写一个加速地址'
        }
        if (form.pathTransformEnabled && form.pathSeparator.trim() === '') {
            next.pathSeparator = '启用路径转换后必须填写分隔符'
        }
        setErrors(next)
        return Object.keys(next).length === 0
    }

    async function handleSave() {
        if (!validate()) {
            return
        }
        setSaving(true)
        setError(null)
        try {
            if (registry === null) {
                const input: CreateRegistryInput = {
                    name: form.name.trim(),
                    kind: form.kind,
                    host: form.host.trim(),
                    insecure_skip_verify: form.insecureSkipVerify,
                    plain_http: form.plainHTTP,
                }
                if (form.kind === 'primary') {
                    input.username = form.username.trim()
                    if (form.password !== '') {
                        input.password = form.password
                    }
                    input.strip_registries = parseStripLines(form.stripText)
                    input.path_transform = form.pathTransformEnabled
                        ? { separator: form.pathSeparator, mode: form.pathMode }
                        : null
                } else {
                    input.mirrors = parseMirrors(form.mirrorsText)
                }
                onSaved(await createRegistry(input))
                return
            }
            const patchBody: UpdateRegistryInput = {
                name: form.name.trim(),
                host: form.host.trim(),
                insecure_skip_verify: form.insecureSkipVerify,
                plain_http: form.plainHTTP,
            }
            if (form.kind === 'primary') {
                patchBody.username = form.username.trim()
                // password 三态：勾选清除（空串）、输入替换、留空保留。
                if (form.clearPassword) {
                    patchBody.password = ''
                } else if (form.password !== '') {
                    patchBody.password = form.password
                }
                patchBody.strip_registries = parseStripLines(form.stripText)
                // path_transform 携带即替换（null = 清除转换配置）。
                patchBody.path_transform = form.pathTransformEnabled
                    ? { separator: form.pathSeparator, mode: form.pathMode }
                    : null
            } else {
                patchBody.mirrors = parseMirrors(form.mirrorsText)
            }
            onSaved(await updateRegistry(registry.id, patchBody))
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setSaving(false)
        }
    }

    const isPrimary = form.kind === 'primary'
    const switchTooltipProps = {
        describeChild: true,
        placement: 'top',
        slotProps: {
            tooltip: {
                sx: {
                    maxWidth: 360,
                    fontSize: 13,
                    lineHeight: 1.6,
                    whiteSpace: 'pre-line',
                    overflowWrap: 'anywhere',
                },
            },
        },
    } as const

    return (
        <Dialog open={open} onClose={onClose} fullWidth maxWidth="sm">
            <DialogTitle>
                {registry === null
                    ? form.kind === 'primary'
                        ? '添加 Registry'
                        : '添加加速源'
                    : form.kind === 'primary'
                      ? '编辑 Registry'
                      : '编辑加速源'}
            </DialogTitle>
            <DialogContent dividers>
                <Stack spacing={2.25} sx={{ pt: 1 }}>
                    {error !== null && <Alert severity="error">{error}</Alert>}
                    {registry === null && (
                        <TextField
                            select
                            label="类型"
                            value={form.kind}
                            onChange={e => setField('kind', e.target.value as RegistryKind)}
                            helperText="primary 是同步目标；mirror 是上游主机的加速器"
                        >
                            <MenuItem value="primary">Registry（同步目标）</MenuItem>
                            <MenuItem value="mirror">加速源（上游镜像加速）</MenuItem>
                        </TextField>
                    )}
                    <TextField
                        label="名称"
                        required
                        value={form.name}
                        onChange={e => setField('name', e.target.value)}
                        error={errors.name !== undefined}
                        helperText={errors.name ?? '任务的目标 Registry 按此名称引用'}
                    />
                    <TextField
                        label={isPrimary ? 'Registry 地址' : '源 Registry 主机'}
                        required
                        value={form.host}
                        onChange={e => setField('host', e.target.value)}
                        error={errors.host !== undefined}
                        helperText={
                            errors.host ??
                            (isPrimary
                                ? '可携带命名空间，如 docker.cnb.cool/lzwang/mirrors'
                                : '如 docker.io；该主机的拉取将优先尝试加速地址')
                        }
                        sx={{ '& input': { fontFamily: 'monospace' } }}
                    />
                    {isPrimary ? (
                        <>
                            <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
                                <TextField
                                    label="用户名"
                                    value={form.username}
                                    onChange={e => setField('username', e.target.value)}
                                    fullWidth
                                />
                                <TextField
                                    label="密码"
                                    type="password"
                                    value={form.password}
                                    onChange={e => setField('password', e.target.value)}
                                    fullWidth
                                    helperText={
                                        registry?.password_set
                                            ? '已设置；留空保留，输入替换'
                                            : undefined
                                    }
                                />
                            </Stack>
                            {registry?.password_set && (
                                <FormControlLabel
                                    control={
                                        <Checkbox
                                            checked={form.clearPassword}
                                            onChange={e =>
                                                setField('clearPassword', e.target.checked)
                                            }
                                        />
                                    }
                                    label="清除已保存的密码"
                                />
                            )}
                            <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
                                <Tooltip
                                    {...switchTooltipProps}
                                    title="通过 HTTPS 连接时跳过服务器证书校验，连接仍使用 TLS 加密。适用于自签证书的内网服务；关闭时正常验证证书。"
                                >
                                    <FormControlLabel
                                        control={
                                            <Switch
                                                checked={form.insecureSkipVerify}
                                                onChange={e =>
                                                    setField('insecureSkipVerify', e.target.checked)
                                                }
                                            />
                                        }
                                        label="跳过 TLS 证书校验"
                                    />
                                </Tooltip>
                                <Tooltip
                                    {...switchTooltipProps}
                                    title="通过明文 HTTP 连接 Registry，适用于未配置 TLS 的内网服务。示例：仓库仅提供 http://registry.lan:5000 时开启。"
                                >
                                    <FormControlLabel
                                        control={
                                            <Switch
                                                checked={form.plainHTTP}
                                                onChange={e =>
                                                    setField('plainHTTP', e.target.checked)
                                                }
                                            />
                                        }
                                        label="使用 Plain HTTP"
                                    />
                                </Tooltip>
                            </Stack>
                            <TextField
                                label="移除源 Registry 前缀"
                                value={form.stripText}
                                onChange={e => setField('stripText', e.target.value)}
                                multiline
                                minRows={2}
                                placeholder={'docker.io/library\nghcr.io'}
                                helperText="每行一个；作为目标 Registry 时移除匹配的源前缀"
                                sx={{ '& textarea': { fontFamily: 'monospace' } }}
                            />
                            <Tooltip
                                {...switchTooltipProps}
                                title="按所选模式转换镜像仓库路径：编码在同步到此 Registry 时将 / 替换为分隔符，解码在从此 Registry 同步时还原 /。示例：未移除源前缀、分隔符为 _ 时，lscr.io/linuxserver/onlyoffice 编码为 lscr.io_linuxserver_onlyoffice。"
                            >
                                <FormControlLabel
                                    control={
                                        <Switch
                                            checked={form.pathTransformEnabled}
                                            onChange={e =>
                                                setField('pathTransformEnabled', e.target.checked)
                                            }
                                        />
                                    }
                                    label="启用镜像路径转换"
                                />
                            </Tooltip>
                            {form.pathTransformEnabled && (
                                <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
                                    <TextField
                                        label="路径分隔符"
                                        value={form.pathSeparator}
                                        onChange={e => setField('pathSeparator', e.target.value)}
                                        error={errors.pathSeparator !== undefined}
                                        helperText={errors.pathSeparator ?? '如 --- 或 _'}
                                        fullWidth
                                    />
                                    <TextField
                                        select
                                        label="转换模式"
                                        value={form.pathMode}
                                        onChange={e => setField('pathMode', e.target.value)}
                                        fullWidth
                                    >
                                        <MenuItem value="both">双向</MenuItem>
                                        <MenuItem value="encode">编码</MenuItem>
                                        <MenuItem value="decode">解码</MenuItem>
                                    </TextField>
                                </Stack>
                            )}
                        </>
                    ) : (
                        <TextField
                            label="加速地址"
                            required
                            value={form.mirrorsText}
                            onChange={e => setField('mirrorsText', e.target.value)}
                            error={errors.mirrorsText !== undefined}
                            helperText={errors.mirrorsText ?? '多个地址请用逗号分隔，按序尝试'}
                            sx={{ '& input': { fontFamily: 'monospace' } }}
                        />
                    )}
                </Stack>
            </DialogContent>
            <DialogActions>
                <Button color="inherit" onClick={onClose} disabled={saving}>
                    取消
                </Button>
                <Button variant="contained" disabled={saving} onClick={() => void handleSave()}>
                    {saving ? '保存中…' : '保存'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
