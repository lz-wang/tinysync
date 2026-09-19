import ContentCopyIcon from '@mui/icons-material/ContentCopy'
import DeleteIcon from '@mui/icons-material/Delete'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'
import {
    Alert,
    Box,
    Chip,
    CircularProgress,
    IconButton,
    Switch,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TableRow,
    TextField,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import {
    deletePublished,
    listPublished,
    type PublishedFileResponse,
    publishedFileURL,
    updatePublished,
} from '../../api'

// PublishedPanel 是发布策略管理视图：list、copy URL、enable/disable、
// 修改过期与删除。serving 语义由后端保证（disabled / 过期 / 缺失
// 一律 404）。
export default function PublishedPanel() {
    const [policies, setPolicies] = useState<PublishedFileResponse[] | null>(null)
    const [error, setError] = useState<string | null>(null)
    const [copied, setCopied] = useState<string | null>(null)

    const reload = useCallback(async () => {
        try {
            setPolicies(await listPublished())
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
        }
    }, [])

    useEffect(() => {
        void reload()
    }, [reload])

    if (policies === null && error === null) {
        return (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
                <CircularProgress />
            </Box>
        )
    }
    if (error !== null) {
        return <Alert severity="error">{error}</Alert>
    }
    if (policies !== null && policies.length === 0) {
        return (
            <Typography variant="body2" color="text.secondary">
                尚未发布任何文件；请在“本地文件”中选择受管理文件后点击“发布”。
            </Typography>
        )
    }

    const toggleEnabled = async (policy: PublishedFileResponse, enabled: boolean) => {
        try {
            await updatePublished(policy.id, { enabled })
            await reload()
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
        }
    }

    const changeExpiry = async (policy: PublishedFileResponse, value: string) => {
        try {
            await updatePublished(policy.id, {
                expires_at: value === '' ? null : new Date(value).toISOString(),
            })
            await reload()
        } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
        }
    }

    const toDatetimeLocal = (iso: string): string => {
        if (iso === '') {
            return ''
        }
        const date = new Date(iso)
        const pad = (n: number) => String(n).padStart(2, '0')
        return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
    }

    const copyURL = async (policy: PublishedFileResponse) => {
        const url = publishedFileURL(policy.public_path)
        await navigator.clipboard.writeText(url)
        setCopied(policy.id)
        window.setTimeout(() => setCopied(null), 1500)
    }

    return (
        <TableContainer>
            <Table size="small">
                <TableHead>
                    <TableRow>
                        <TableCell>公开路径</TableCell>
                        <TableCell>本地文件</TableCell>
                        <TableCell>启用</TableCell>
                        <TableCell>过期时间</TableCell>
                        <TableCell align="right">操作</TableCell>
                    </TableRow>
                </TableHead>
                <TableBody>
                    {policies?.map(policy => (
                        <TableRow key={policy.id}>
                            <TableCell sx={{ fontFamily: 'monospace' }}>
                                {policy.public_path}
                            </TableCell>
                            <TableCell
                                sx={{
                                    fontFamily: 'monospace',
                                    maxWidth: 320,
                                    overflow: 'hidden',
                                    textOverflow: 'ellipsis',
                                }}
                            >
                                {policy.local_path}
                            </TableCell>
                            <TableCell>
                                <Switch
                                    checked={policy.enabled}
                                    onChange={event =>
                                        void toggleEnabled(policy, event.target.checked)
                                    }
                                    size="small"
                                />
                            </TableCell>
                            <TableCell>
                                <TextField
                                    type="datetime-local"
                                    size="small"
                                    variant="standard"
                                    value={toDatetimeLocal(policy.expires_at)}
                                    onChange={event =>
                                        void changeExpiry(policy, event.target.value)
                                    }
                                />
                            </TableCell>
                            <TableCell align="right">
                                {copied === policy.id && (
                                    <Chip size="small" label="已复制" sx={{ mr: 1 }} />
                                )}
                                <IconButton
                                    size="small"
                                    aria-label="复制 URL"
                                    onClick={() => void copyURL(policy)}
                                >
                                    <ContentCopyIcon fontSize="small" />
                                </IconButton>
                                <IconButton
                                    size="small"
                                    aria-label="打开 URL"
                                    href={publishedFileURL(policy.public_path)}
                                    target="_blank"
                                    rel="noreferrer"
                                >
                                    <OpenInNewIcon fontSize="small" />
                                </IconButton>
                                <IconButton
                                    size="small"
                                    aria-label="删除发布规则"
                                    onClick={() => {
                                        void deletePublished(policy.id).then(reload)
                                    }}
                                >
                                    <DeleteIcon fontSize="small" />
                                </IconButton>
                            </TableCell>
                        </TableRow>
                    ))}
                </TableBody>
            </Table>
        </TableContainer>
    )
}
