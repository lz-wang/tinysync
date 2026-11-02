import AddTaskIcon from '@mui/icons-material/AddTask'
import BoltIcon from '@mui/icons-material/Bolt'
import {
    Box,
    Button,
    Checkbox,
    Chip,
    CircularProgress,
    IconButton,
    Stack,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TableRow,
    TableSortLabel,
    Tooltip,
    Typography,
} from '@mui/material'
import { useEffect, useMemo, useState } from 'react'
import { Link as RouterLink } from 'react-router-dom'
import { type ImageListItemResponse, runJob } from '../../api'
import { useToast } from '../../app/toast'
import { formatDateTime } from '../history/shared'
import { lastStatusChipProps } from './shared'

export type ImageSortField = 'source' | 'job_name' | 'target' | 'last_sync_time' | 'result'
export type SortDirection = 'asc' | 'desc'

// keyFor 是行的稳定键：同一源镜像可出现在多个 oci 任务下。
const keyFor = (item: ImageListItemResponse) => `${item.job_id}\u0000${item.source}`

interface ImageStatusTableProps {
    images: ImageListItemResponse[]
    loading: boolean
    onChanged: () => void
    // onAddToTask 打开预填的 JobDialog（创建新 oci 任务或并入既有任务）。
    onAddToTask: (images: ImageListItemResponse[]) => void
    sortField: ImageSortField | null
    sortDirection: SortDirection
    onSort: (field: ImageSortField) => void
}

// ImageStatusTable 是镜像同步状态表（oci-sync ImageTable 移植）：
// 行 / 批量「立即同步」走 runJob 的 src_images 子集语义（只同步所选
// 镜像，不改任务配置）；「添加到任务」打开预填 JobDialog。批量 move /
// delete 不移植（R9）。
export function ImageStatusTable({
    images,
    loading,
    onChanged,
    onAddToTask,
    sortField,
    sortDirection,
    onSort,
}: ImageStatusTableProps) {
    const toast = useToast()
    const [selected, setSelected] = useState<string[]>([])
    const [syncing, setSyncing] = useState(false)
    const selectedImages = useMemo(
        () => images.filter(item => selected.includes(keyFor(item))),
        [images, selected],
    )
    // 列表刷新后清掉已消失行的选中态。
    useEffect(() => {
        setSelected(prev => prev.filter(key => images.some(item => keyFor(item) === key)))
    }, [images])
    const allSelected = images.length > 0 && images.every(item => selected.includes(keyFor(item)))
    const sortableLabel = (label: string, field: ImageSortField) => (
        <TableSortLabel
            active={sortField === field}
            direction={sortField === field ? sortDirection : 'asc'}
            onClick={() => onSort(field)}
        >
            {label}
        </TableSortLabel>
    )

    // sync 触发所选镜像的子集同步：按任务分组，每组一次 runJob 调用，
    // src_images 为该组选中的源镜像集合（后端校验子集合法性并展开）。
    const sync = async (items: ImageListItemResponse[]) => {
        if (items.length === 0) {
            return
        }
        const groups = new Map<string, string[]>()
        for (const item of items) {
            groups.set(item.job_id, [...(groups.get(item.job_id) ?? []), item.source])
        }
        setSyncing(true)
        try {
            await Promise.all(
                Array.from(groups, ([jobId, sources]) =>
                    runJob(jobId, { src_images: [...new Set(sources)] }),
                ),
            )
            toast.success(`已开始同步 ${items.length} 个镜像`)
            setSelected([])
            onChanged()
        } catch (e) {
            toast.error(e instanceof Error ? e.message : String(e))
        } finally {
            setSyncing(false)
        }
    }

    return (
        <>
            {selectedImages.length > 0 && (
                <Stack
                    direction={{ xs: 'column', md: 'row' }}
                    spacing={1}
                    sx={{
                        p: 1.5,
                        alignItems: { md: 'center' },
                        borderBottom: 1,
                        borderColor: 'divider',
                    }}
                >
                    <Typography variant="body2">已选 {selectedImages.length} 项</Typography>
                    <Button
                        size="small"
                        variant="outlined"
                        startIcon={<BoltIcon />}
                        onClick={() => void sync(selectedImages)}
                        disabled={syncing || selectedImages.some(item => !item.job_enabled)}
                    >
                        批量同步
                    </Button>
                    <Button
                        size="small"
                        variant="outlined"
                        startIcon={<AddTaskIcon />}
                        onClick={() => onAddToTask(selectedImages)}
                    >
                        添加到任务
                    </Button>
                </Stack>
            )}
            <TableContainer sx={{ flexGrow: 1, minHeight: 0, overflow: 'auto' }}>
                <Table size="small" sx={{ minWidth: 960 }}>
                    <TableHead>
                        <TableRow>
                            <TableCell padding="checkbox">
                                <Checkbox
                                    size="small"
                                    checked={allSelected}
                                    indeterminate={!allSelected && selectedImages.length > 0}
                                    onChange={event =>
                                        setSelected(event.target.checked ? images.map(keyFor) : [])
                                    }
                                />
                            </TableCell>
                            <TableCell>{sortableLabel('镜像', 'source')}</TableCell>
                            <TableCell>{sortableLabel('任务', 'job_name')}</TableCell>
                            <TableCell>{sortableLabel('目标', 'target')}</TableCell>
                            <TableCell>{sortableLabel('最后同步', 'last_sync_time')}</TableCell>
                            <TableCell>{sortableLabel('结果', 'result')}</TableCell>
                            <TableCell align="center">操作</TableCell>
                        </TableRow>
                    </TableHead>
                    <TableBody>
                        {images.map(item => {
                            const props = lastStatusChipProps(item.last_status)
                            return (
                                <TableRow
                                    key={keyFor(item)}
                                    hover
                                    selected={selected.includes(keyFor(item))}
                                >
                                    <TableCell padding="checkbox">
                                        <Checkbox
                                            size="small"
                                            checked={selected.includes(keyFor(item))}
                                            onChange={event =>
                                                setSelected(prev =>
                                                    event.target.checked
                                                        ? [...prev, keyFor(item)]
                                                        : prev.filter(key => key !== keyFor(item)),
                                                )
                                            }
                                        />
                                    </TableCell>
                                    <TableCell
                                        sx={{ fontFamily: 'monospace', wordBreak: 'break-all' }}
                                    >
                                        {item.source}
                                    </TableCell>
                                    <TableCell>
                                        <Typography
                                            component={RouterLink}
                                            to="/jobs"
                                            variant="body2"
                                            sx={{
                                                color: 'primary.main',
                                                fontWeight: 500,
                                                textDecoration: 'none',
                                                '&:hover': { textDecoration: 'underline' },
                                            }}
                                        >
                                            {item.job_name}
                                        </Typography>
                                    </TableCell>
                                    <TableCell>
                                        <Stack
                                            direction="row"
                                            spacing={0.5}
                                            sx={{ flexWrap: 'wrap', gap: 0.5 }}
                                        >
                                            {item.dst_registries.map(registry => (
                                                <Chip
                                                    key={registry}
                                                    label={registry}
                                                    size="small"
                                                    variant="outlined"
                                                />
                                            ))}
                                        </Stack>
                                    </TableCell>
                                    <TableCell>
                                        <Typography
                                            variant="body2"
                                            sx={{ color: 'text.secondary', whiteSpace: 'nowrap' }}
                                        >
                                            {formatDateTime(item.last_sync_time)}
                                        </Typography>
                                    </TableCell>
                                    <TableCell>
                                        <Chip
                                            label={props.label}
                                            color={props.color}
                                            size="small"
                                            variant="outlined"
                                        />
                                    </TableCell>
                                    <TableCell align="center">
                                        <Stack
                                            direction="row"
                                            spacing={0.25}
                                            sx={{ justifyContent: 'center' }}
                                        >
                                            <Tooltip
                                                title={
                                                    item.job_enabled
                                                        ? '立即同步（仅本次运行的子集）'
                                                        : '任务已停用，无法同步'
                                                }
                                            >
                                                <span>
                                                    <IconButton
                                                        size="small"
                                                        color="primary"
                                                        aria-label={`同步 ${item.source}`}
                                                        disabled={!item.job_enabled || syncing}
                                                        onClick={() => void sync([item])}
                                                    >
                                                        {syncing ? (
                                                            <CircularProgress
                                                                size={18}
                                                                color="inherit"
                                                            />
                                                        ) : (
                                                            <BoltIcon fontSize="small" />
                                                        )}
                                                    </IconButton>
                                                </span>
                                            </Tooltip>
                                            <Tooltip title="添加到任务">
                                                <IconButton
                                                    size="small"
                                                    aria-label={`添加到任务 ${item.source}`}
                                                    onClick={() => onAddToTask([item])}
                                                >
                                                    <AddTaskIcon fontSize="small" />
                                                </IconButton>
                                            </Tooltip>
                                        </Stack>
                                    </TableCell>
                                </TableRow>
                            )
                        })}
                        {images.length === 0 && (
                            <TableRow>
                                <TableCell colSpan={7}>
                                    <Box sx={{ py: 6, textAlign: 'center' }}>
                                        {loading ? (
                                            <CircularProgress />
                                        ) : (
                                            <Typography color="text.secondary">
                                                暂无镜像。创建 OCI 任务后，其源镜像会出现在这里。
                                            </Typography>
                                        )}
                                    </Box>
                                </TableCell>
                            </TableRow>
                        )}
                    </TableBody>
                </Table>
            </TableContainer>
        </>
    )
}
