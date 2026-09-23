import AddOutlinedIcon from '@mui/icons-material/AddOutlined'
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutlined'
import EditOutlinedIcon from '@mui/icons-material/EditOutlined'
import {
    Box,
    Button,
    Chip,
    IconButton,
    Paper,
    Stack,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TableRow,
    Tooltip,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import { type CredentialResponse, listCredentials, listSources, type SourceResponse } from '../api'
import { formatTime } from '../app/formatTime'
import { useToast } from '../app/toast'
import { usePageTitle } from '../app/usePageTitle'
import CredentialDialog from '../features/credentials/CredentialDialog'
import DeleteCredentialDialog from '../features/credentials/DeleteCredentialDialog'

// CredentialsPage 是凭据管理页：列表（名称 / 公钥指纹 / 带口令 /
// 引用数 / 更新时间）、创建 / 编辑（secret 整体替换）、删除（被引用
// fail-closed）。操作反馈统一走全局 toast。
export default function CredentialsPage() {
    usePageTitle('凭据')
    const toast = useToast()
    const [credentials, setCredentials] = useState<CredentialResponse[] | null>(null)
    const [sources, setSources] = useState<SourceResponse[]>([])
    const [loadError, setLoadError] = useState<string | null>(null)
    const [dialogOpen, setDialogOpen] = useState(false)
    const [editing, setEditing] = useState<CredentialResponse | null>(null)
    const [deleting, setDeleting] = useState<CredentialResponse | null>(null)

    const refresh = useCallback(async () => {
        setLoadError(null)
        try {
            const [credentialList, sourceList] = await Promise.all([
                listCredentials(),
                listSources(),
            ])
            setCredentials(credentialList)
            setSources(sourceList)
        } catch (error) {
            setLoadError(error instanceof Error ? error.message : String(error))
            toast.error(error instanceof Error ? error.message : String(error))
        }
    }, [toast])

    useEffect(() => {
        void refresh()
    }, [refresh])

    const handleSaved = async (saved: CredentialResponse) => {
        setDialogOpen(false)
        setEditing(null)
        toast.success(credentialEditToastText(saved))
        await refresh()
    }

    const handleDeleted = async () => {
        setDeleting(null)
        toast.success('凭据已删除')
        await refresh()
    }

    return (
        <Paper variant="outlined" sx={{ p: 2 }}>
            <Stack spacing={2}>
                <Stack
                    direction="row"
                    sx={{ alignItems: 'center', justifyContent: 'space-between' }}
                >
                    <Typography variant="body2" color="text.secondary">
                        凭据是命名的 secret 记录，可被多个 SFTP 源引用；换钥一次生效全部。
                    </Typography>
                    <Button
                        variant="contained"
                        startIcon={<AddOutlinedIcon />}
                        onClick={() => {
                            setEditing(null)
                            setDialogOpen(true)
                        }}
                    >
                        创建凭据
                    </Button>
                </Stack>
                {loadError !== null && (
                    <Typography color="error" variant="body2">
                        {loadError}
                    </Typography>
                )}
                <TableContainer component={Box}>
                    <Table size="small">
                        <TableHead>
                            <TableRow>
                                <TableCell>名称</TableCell>
                                <TableCell>公钥指纹</TableCell>
                                <TableCell>口令</TableCell>
                                <TableCell align="right">被引用</TableCell>
                                <TableCell>更新时间</TableCell>
                                <TableCell align="right">操作</TableCell>
                            </TableRow>
                        </TableHead>
                        <TableBody>
                            {(credentials ?? []).map(credential => (
                                <TableRow key={credential.id} hover>
                                    <TableCell>{credential.name}</TableCell>
                                    <TableCell>
                                        <Typography
                                            variant="body2"
                                            sx={{ fontFamily: 'monospace' }}
                                        >
                                            {credential.fingerprint}
                                        </Typography>
                                    </TableCell>
                                    <TableCell>
                                        <Chip
                                            size="small"
                                            label={credential.has_passphrase ? '带口令' : '无'}
                                            color={
                                                credential.has_passphrase ? 'default' : 'default'
                                            }
                                            variant={
                                                credential.has_passphrase ? 'filled' : 'outlined'
                                            }
                                        />
                                    </TableCell>
                                    <TableCell align="right">
                                        {credential.referenced_by > 0 ? (
                                            <Chip
                                                size="small"
                                                label={`${credential.referenced_by} 个源`}
                                                color="primary"
                                                variant="outlined"
                                            />
                                        ) : (
                                            <Typography variant="body2" color="text.secondary">
                                                未引用
                                            </Typography>
                                        )}
                                    </TableCell>
                                    <TableCell>{formatTime(credential.updated_at)}</TableCell>
                                    <TableCell align="right">
                                        <Tooltip title="编辑">
                                            <IconButton
                                                size="small"
                                                aria-label={`编辑 ${credential.name}`}
                                                onClick={() => {
                                                    setEditing(credential)
                                                    setDialogOpen(true)
                                                }}
                                            >
                                                <EditOutlinedIcon fontSize="small" />
                                            </IconButton>
                                        </Tooltip>
                                        <Tooltip title="删除">
                                            <IconButton
                                                size="small"
                                                aria-label={`删除 ${credential.name}`}
                                                onClick={() => setDeleting(credential)}
                                            >
                                                <DeleteOutlineIcon fontSize="small" />
                                            </IconButton>
                                        </Tooltip>
                                    </TableCell>
                                </TableRow>
                            ))}
                            {credentials !== null && credentials.length === 0 && (
                                <TableRow>
                                    <TableCell colSpan={6}>
                                        <Typography
                                            variant="body2"
                                            color="text.secondary"
                                            align="center"
                                            sx={{ py: 2 }}
                                        >
                                            还没有凭据。创建一把 SSH 私钥凭据，供多个 SFTP 源引用。
                                        </Typography>
                                    </TableCell>
                                </TableRow>
                            )}
                        </TableBody>
                    </Table>
                </TableContainer>
            </Stack>
            <CredentialDialog
                open={dialogOpen}
                credential={editing}
                onClose={() => {
                    setDialogOpen(false)
                    setEditing(null)
                }}
                onSaved={saved => void handleSaved(saved)}
            />
            <DeleteCredentialDialog
                credential={deleting}
                sources={sources}
                onClose={() => setDeleting(null)}
                onDeleted={() => void handleDeleted()}
            />
        </Paper>
    )
}

function credentialEditToastText(saved: CredentialResponse): string {
    return saved.name === '' ? '凭据已保存' : `凭据“${saved.name}”已保存`
}
