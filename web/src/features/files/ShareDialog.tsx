import ContentCopyOutlinedIcon from '@mui/icons-material/ContentCopyOutlined'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'
import {
    Alert,
    Button,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControlLabel,
    IconButton,
    Stack,
    Switch,
    TextField,
    Tooltip,
} from '@mui/material'
import { useState } from 'react'
import { createShare, type ShareResponse, shareEntryURL } from '../../api'

// ShareDialog 是共享创建对话框：目标为 Local 浏览中选中的文件、
// 目录或本地根本身；共享名称可选（即 URL slug，留空随机生成）。
// 创建成功后先内嵌展示完整公开链接（复制 / 打开），再由用户关闭。
export default function ShareDialog({
    open,
    jobId,
    targetPath,
    onClose,
    onCreated,
}: {
    open: boolean
    jobId: string
    // targetPath 是 LocalRoot 内逻辑路径；"/" 即共享整个本地根。
    targetPath: string
    onClose: () => void
    onCreated: () => void
}) {
    const [name, setName] = useState('')
    const [expiresAt, setExpiresAt] = useState('')
    const [enabled, setEnabled] = useState(true)
    const [saving, setSaving] = useState(false)
    const [error, setError] = useState<string | null>(null)
    const [created, setCreated] = useState<ShareResponse | null>(null)
    const [copied, setCopied] = useState(false)

    const handleSave = async () => {
        setSaving(true)
        setError(null)
        try {
            const share = await createShare({
                job_id: jobId,
                path: targetPath,
                name: name.trim() === '' ? undefined : name.trim(),
                enabled,
                expires_at: expiresAt === '' ? undefined : new Date(expiresAt).toISOString(),
            })
            setCreated(share)
            onCreated()
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
        } finally {
            setSaving(false)
        }
    }

    const link = created === null ? '' : shareEntryURL(created)
    const copyLink = async () => {
        await navigator.clipboard.writeText(link)
        setCopied(true)
        window.setTimeout(() => setCopied(false), 1500)
    }

    return (
        <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
            <DialogTitle>{created === null ? '共享' : '共享已创建'}</DialogTitle>
            <DialogContent>
                {created !== null ? (
                    <Stack spacing={2} sx={{ mt: 1 }}>
                        <Alert severity="success">
                            公开链接已生成{created.is_dir ? '（目录内容整棵公开）' : ''}。
                        </Alert>
                        <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
                            <TextField
                                label="公开链接"
                                value={link}
                                fullWidth
                                slotProps={{ htmlInput: { readOnly: true } }}
                                sx={{ '& input': { fontFamily: 'monospace' } }}
                            />
                            <Tooltip title="复制链接">
                                <IconButton
                                    aria-label="复制公开链接"
                                    onClick={() => void copyLink()}
                                    size="small"
                                >
                                    <ContentCopyOutlinedIcon fontSize="small" />
                                </IconButton>
                            </Tooltip>
                            <Tooltip title="打开">
                                <IconButton
                                    aria-label="打开公开链接"
                                    href={link}
                                    target="_blank"
                                    rel="noreferrer"
                                    size="small"
                                >
                                    <OpenInNewIcon fontSize="small" />
                                </IconButton>
                            </Tooltip>
                        </Stack>
                        {copied && <Alert severity="info">链接已复制</Alert>}
                    </Stack>
                ) : (
                    <Stack spacing={2} sx={{ mt: 1 }}>
                        {error !== null && <Alert severity="error">{error}</Alert>}
                        <TextField
                            label="共享目标"
                            value={targetPath}
                            disabled
                            sx={{ '& input': { fontFamily: 'monospace' } }}
                            helperText="共享目录会公开整棵目录内容（含未同步文件）"
                        />
                        <TextField
                            label="共享名称"
                            value={name}
                            onChange={event => setName(event.target.value)}
                            sx={{ '& input': { fontFamily: 'monospace' } }}
                            helperText="留空自动生成；字母或数字开头，可含 - 与 _，最长 64 字符"
                        />
                        <TextField
                            label="过期时间"
                            type="datetime-local"
                            value={expiresAt}
                            onChange={event => setExpiresAt(event.target.value)}
                            helperText="留空表示永不过期"
                        />
                        <FormControlLabel
                            control={
                                <Switch
                                    checked={enabled}
                                    onChange={event => setEnabled(event.target.checked)}
                                />
                            }
                            label="启用"
                        />
                    </Stack>
                )}
            </DialogContent>
            <DialogActions>
                <Button onClick={onClose} disabled={saving}>
                    {created === null ? '取消' : '关闭'}
                </Button>
                {created === null && (
                    <Button onClick={() => void handleSave()} variant="contained" disabled={saving}>
                        {saving ? '共享中…' : '共享'}
                    </Button>
                )}
            </DialogActions>
        </Dialog>
    )
}
