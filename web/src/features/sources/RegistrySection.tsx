import AddOutlinedIcon from '@mui/icons-material/AddOutlined'
import DeleteOutlineIcon from '@mui/icons-material/DeleteOutlined'
import EditOutlinedIcon from '@mui/icons-material/EditOutlined'
import {
    Box,
    Button,
    Chip,
    IconButton,
    Stack,
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableRow,
    Tooltip,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import { deleteRegistry, listRegistries, type RegistryKind, type RegistryResponse } from '../../api'
import { ConfirmDialog } from '../../app/ConfirmDialog'
import { useToast } from '../../app/toast'
import RegistryDialog from './RegistryDialog'

// RegistrySection 是「同步源」页的 Registry 分区（oci-sync 设置面板
// 移植，存储改为逐项 CRUD）：primary 表（同步目标）与 mirror 表（上游
// 加速器）。oci 任务的目标 Registry（dst_servers）引用这里的配置名。
export default function RegistrySection() {
    const toast = useToast()
    const [registries, setRegistries] = useState<RegistryResponse[] | null>(null)
    const [dialogOpen, setDialogOpen] = useState(false)
    const [editing, setEditing] = useState<RegistryResponse | null>(null)
    const [createKind, setCreateKind] = useState<RegistryKind>('primary')
    const [deleting, setDeleting] = useState<RegistryResponse | null>(null)

    const reload = useCallback(async () => {
        try {
            setRegistries(await listRegistries())
        } catch (e) {
            toast.error(e instanceof Error ? e.message : String(e))
        }
    }, [toast])
    useEffect(() => {
        void reload()
    }, [reload])

    const openCreate = (kind: RegistryKind) => {
        setEditing(null)
        setCreateKind(kind)
        setDialogOpen(true)
    }
    const openEdit = (registry: RegistryResponse) => {
        setEditing(registry)
        setDialogOpen(true)
    }
    const handleSaved = () => {
        setDialogOpen(false)
        setEditing(null)
        void reload()
    }
    const handleDelete = async () => {
        if (deleting === null) {
            return
        }
        try {
            await deleteRegistry(deleting.id)
            toast.success(`已删除 ${deleting.name}`)
            setRegistries(prev => prev?.filter(row => row.id !== deleting.id) ?? null)
        } catch (e) {
            // 仍被 oci 任务引用（409）等失败在此提示。
            toast.error(e instanceof Error ? e.message : String(e))
        } finally {
            setDeleting(null)
        }
    }

    const primaries = (registries ?? []).filter(row => row.kind === 'primary')
    const mirrors = (registries ?? []).filter(row => row.kind === 'mirror')
    return (
        <Stack spacing={2}>
            <Stack
                direction={{ xs: 'column', md: 'row' }}
                spacing={1}
                sx={{ alignItems: { md: 'center' }, justifyContent: 'space-between' }}
            >
                <Box>
                    <Typography variant="subtitle1" sx={{ fontWeight: 650 }}>
                        Registry 配置
                    </Typography>
                    <Typography variant="caption" color="text.secondary">
                        OCI 镜像同步的目标仓库（primary）与上游加速器（mirror）。
                    </Typography>
                </Box>
                <Stack direction="row" spacing={1}>
                    <Button
                        variant="outlined"
                        startIcon={<AddOutlinedIcon />}
                        onClick={() => openCreate('mirror')}
                    >
                        添加加速源
                    </Button>
                    <Button
                        variant="contained"
                        startIcon={<AddOutlinedIcon />}
                        onClick={() => openCreate('primary')}
                    >
                        添加 Registry
                    </Button>
                </Stack>
            </Stack>
            <Stack spacing={3}>
                <Box
                    sx={{ border: 1, borderColor: 'divider', borderRadius: 1.5, overflowX: 'auto' }}
                >
                    <Table size="small">
                        <TableHead>
                            <TableRow>
                                <TableCell>名称</TableCell>
                                <TableCell>Registry 地址</TableCell>
                                <TableCell>选项</TableCell>
                                <TableCell align="center">操作</TableCell>
                            </TableRow>
                        </TableHead>
                        <TableBody>
                            {primaries.map(registry => (
                                <TableRow hover key={registry.id}>
                                    <TableCell sx={{ fontWeight: 600 }}>{registry.name}</TableCell>
                                    <TableCell
                                        sx={{ fontFamily: 'monospace', wordBreak: 'break-all' }}
                                    >
                                        {registry.host}
                                        {registry.password_set && (
                                            <Tooltip title="已保存凭据">
                                                <Chip
                                                    label="凭据"
                                                    size="small"
                                                    variant="outlined"
                                                    sx={{ ml: 1 }}
                                                />
                                            </Tooltip>
                                        )}
                                    </TableCell>
                                    <TableCell>
                                        <Stack
                                            direction="row"
                                            spacing={0.5}
                                            sx={{ flexWrap: 'wrap', gap: 0.5 }}
                                        >
                                            {registry.insecure_skip_verify && (
                                                <Chip
                                                    label="跳过 TLS 校验"
                                                    size="small"
                                                    variant="outlined"
                                                />
                                            )}
                                            {registry.plain_http && (
                                                <Chip
                                                    label="Plain HTTP"
                                                    size="small"
                                                    variant="outlined"
                                                />
                                            )}
                                            {registry.path_transform !== null && (
                                                <Chip
                                                    label={`路径转换 ${registry.path_transform.separator}`}
                                                    size="small"
                                                    variant="outlined"
                                                />
                                            )}
                                            {(registry.strip_registries ?? []).length > 0 && (
                                                <Chip
                                                    label={`移除前缀 ×${(registry.strip_registries ?? []).length}`}
                                                    size="small"
                                                    variant="outlined"
                                                />
                                            )}
                                        </Stack>
                                    </TableCell>
                                    <TableCell align="center">
                                        <Stack
                                            direction="row"
                                            spacing={0.25}
                                            sx={{ justifyContent: 'center' }}
                                        >
                                            <Tooltip title="编辑">
                                                <IconButton
                                                    size="small"
                                                    aria-label={`编辑 ${registry.name}`}
                                                    onClick={() => openEdit(registry)}
                                                >
                                                    <EditOutlinedIcon fontSize="small" />
                                                </IconButton>
                                            </Tooltip>
                                            <Tooltip title="删除">
                                                <IconButton
                                                    size="small"
                                                    color="error"
                                                    aria-label={`删除 ${registry.name}`}
                                                    onClick={() => setDeleting(registry)}
                                                >
                                                    <DeleteOutlineIcon fontSize="small" />
                                                </IconButton>
                                            </Tooltip>
                                        </Stack>
                                    </TableCell>
                                </TableRow>
                            ))}
                            {primaries.length === 0 && (
                                <TableRow>
                                    <TableCell colSpan={4}>
                                        <Typography
                                            variant="body2"
                                            color="text.secondary"
                                            sx={{ py: 3, textAlign: 'center' }}
                                        >
                                            暂无 Registry 配置。OCI 任务需要一个目标 Registry。
                                        </Typography>
                                    </TableCell>
                                </TableRow>
                            )}
                        </TableBody>
                    </Table>
                </Box>
                <Box
                    sx={{ border: 1, borderColor: 'divider', borderRadius: 1.5, overflowX: 'auto' }}
                >
                    <Table size="small">
                        <TableHead>
                            <TableRow>
                                <TableCell>名称</TableCell>
                                <TableCell>源 Registry</TableCell>
                                <TableCell>加速地址</TableCell>
                                <TableCell align="center">操作</TableCell>
                            </TableRow>
                        </TableHead>
                        <TableBody>
                            {mirrors.map(mirror => (
                                <TableRow hover key={mirror.id}>
                                    <TableCell sx={{ fontWeight: 600 }}>{mirror.name}</TableCell>
                                    <TableCell sx={{ fontFamily: 'monospace' }}>
                                        {mirror.host}
                                    </TableCell>
                                    <TableCell
                                        sx={{ fontFamily: 'monospace', wordBreak: 'break-all' }}
                                    >
                                        {(mirror.mirrors ?? []).join(', ')}
                                    </TableCell>
                                    <TableCell align="center">
                                        <Stack
                                            direction="row"
                                            spacing={0.25}
                                            sx={{ justifyContent: 'center' }}
                                        >
                                            <Tooltip title="编辑">
                                                <IconButton
                                                    size="small"
                                                    aria-label={`编辑 ${mirror.name}`}
                                                    onClick={() => openEdit(mirror)}
                                                >
                                                    <EditOutlinedIcon fontSize="small" />
                                                </IconButton>
                                            </Tooltip>
                                            <Tooltip title="删除">
                                                <IconButton
                                                    size="small"
                                                    color="error"
                                                    aria-label={`删除 ${mirror.name}`}
                                                    onClick={() => setDeleting(mirror)}
                                                >
                                                    <DeleteOutlineIcon fontSize="small" />
                                                </IconButton>
                                            </Tooltip>
                                        </Stack>
                                    </TableCell>
                                </TableRow>
                            ))}
                            {mirrors.length === 0 && (
                                <TableRow>
                                    <TableCell colSpan={4}>
                                        <Typography
                                            variant="body2"
                                            color="text.secondary"
                                            sx={{ py: 3, textAlign: 'center' }}
                                        >
                                            暂无加速源。为 docker.io
                                            等上游配置加速器以提升拉取速度。
                                        </Typography>
                                    </TableCell>
                                </TableRow>
                            )}
                        </TableBody>
                    </Table>
                </Box>
            </Stack>
            <RegistryDialog
                open={dialogOpen}
                registry={editing}
                createKind={createKind}
                onClose={() => {
                    setDialogOpen(false)
                    setEditing(null)
                }}
                onSaved={handleSaved}
            />
            <ConfirmDialog
                open={deleting !== null}
                title={deleting?.kind === 'mirror' ? '删除加速源' : '删除 Registry'}
                message={
                    deleting === null
                        ? ''
                        : `确定删除 ${deleting.kind === 'mirror' ? '加速源' : 'Registry'} “${deleting.name}”吗？仍被 OCI 任务引用时会删除失败。`
                }
                confirmText="删除"
                confirmColor="error"
                onClose={() => setDeleting(null)}
                onConfirm={() => void handleDelete()}
            />
        </Stack>
    )
}
