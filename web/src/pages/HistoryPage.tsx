import {
    Box,
    Card,
    CardContent,
    Chip,
    CircularProgress,
    MenuItem,
    Pagination,
    Stack,
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
import { Link as RouterLink } from 'react-router-dom'
import type { JobKind, RunRecordResponse } from '../api'
import { isFileRunResult, isOciRunResult, listRuns } from '../api'
import { useToast } from '../app/toast'
import { usePageTitle } from '../app/usePageTitle'
import {
    formatBytes,
    formatDateTime,
    formatDuration,
    RunRecordChip,
} from '../features/history/shared'

// pageSize 是历史列表每页条数（与服务端上限一致的默认值）。
const pageSize = 50

// HistoryPage 提供全局执行历史：所有任务最近的成功 / 失败 / 跳过一览，
// 支持按任务类型（后端 kind 参数）过滤，分页浏览，点击行进入运行详情。
// 变更 / 传输量按任务类型从 result_json 渲染（oci 为镜像复制摘要）。
export default function HistoryPage() {
    usePageTitle('执行历史')
    const toast = useToast()
    const [runs, setRuns] = useState<RunRecordResponse[] | null>(null)
    const [total, setTotal] = useState(0)
    const [page, setPage] = useState(1)
    const [kindFilter, setKindFilter] = useState<'all' | JobKind>('all')
    const [error, setError] = useState<string | null>(null)

    const load = useCallback(async (targetPage: number, kind: 'all' | JobKind) => {
        const data = await listRuns({
            limit: pageSize,
            offset: (targetPage - 1) * pageSize,
            ...(kind === 'all' ? {} : { kind }),
        })
        setRuns(data.runs)
        setTotal(data.total)
    }, [])

    useEffect(() => {
        let cancelled = false
        setError(null)
        load(page, kindFilter).catch(e => {
            if (!cancelled) {
                const message = e instanceof Error ? e.message : String(e)
                setError(message)
                toast.error(message)
            }
        })
        return () => {
            cancelled = true
        }
    }, [load, page, kindFilter, toast])

    return (
        <Card variant="outlined">
            <CardContent>
                <Stack spacing={2}>
                    <TextField
                        select
                        size="small"
                        label="任务类型"
                        value={kindFilter}
                        onChange={event => {
                            setKindFilter(event.target.value as 'all' | JobKind)
                            setPage(1)
                        }}
                        sx={{ alignSelf: 'flex-start', minWidth: 130 }}
                    >
                        <MenuItem value="all">全部类型</MenuItem>
                        <MenuItem value="file">文件同步</MenuItem>
                        <MenuItem value="oci">OCI 镜像</MenuItem>
                    </TextField>
                    {runs === null && error === null ? (
                        <CircularProgress size={24} aria-label="加载中" />
                    ) : runs !== null && runs.length === 0 ? (
                        <Typography variant="body2" color="text.secondary">
                            暂无同步记录。请手动运行任务或等待计划触发。
                        </Typography>
                    ) : runs !== null ? (
                        <>
                            <RunsTable runs={runs} />
                            {total > pageSize && (
                                <Box sx={{ display: 'flex', justifyContent: 'center' }}>
                                    <Pagination
                                        count={Math.ceil(total / pageSize)}
                                        page={page}
                                        onChange={(_, value) => setPage(value)}
                                        size="small"
                                    />
                                </Box>
                            )}
                        </>
                    ) : (
                        // 初始加载失败：详情已在 toast 中展示，区域保留简短失败文案。
                        <Typography variant="body2" color="text.secondary">
                            执行历史加载失败。
                        </Typography>
                    )}
                </Stack>
            </CardContent>
        </Card>
    )
}

// changesCell 按任务类型渲染「变更」列：file 为文件计数，oci 为镜像
// 复制计数（成功 / 跳过 / 失败）。
function changesCell(run: RunRecordResponse): string {
    if (isFileRunResult(run.result)) {
        const result = run.result
        return `${result.files_created}+ ${result.files_updated}~ ${result.files_deleted}- ${result.files_skipped}↷`
    }
    if (isOciRunResult(run.result)) {
        const result = run.result
        return `成 ${result.success_count} · 跳 ${result.skipped_count} · 败 ${result.failure_count}`
    }
    return '—'
}

// bytesCell 按任务类型渲染「传输量」列。
function bytesCell(run: RunRecordResponse): string {
    if (isFileRunResult(run.result)) {
        return formatBytes(run.result.bytes_transferred)
    }
    if (isOciRunResult(run.result)) {
        return formatBytes(run.result.transferred_bytes ?? 0)
    }
    return '—'
}

function RunsTable({ runs }: { runs: RunRecordResponse[] }) {
    return (
        <TableContainer>
            <Table size="small">
                <TableHead>
                    <TableRow>
                        <TableCell>时间</TableCell>
                        <TableCell>任务</TableCell>
                        <TableCell>类型</TableCell>
                        <TableCell>触发方式</TableCell>
                        <TableCell>状态</TableCell>
                        <TableCell>耗时</TableCell>
                        <TableCell>变更</TableCell>
                        <TableCell align="right">传输量</TableCell>
                    </TableRow>
                </TableHead>
                <TableBody>
                    {runs.map(run => (
                        <TableRow
                            key={run.id}
                            hover
                            component={RouterLink}
                            to={`/history/${run.id}`}
                            sx={{ textDecoration: 'none', color: 'inherit', cursor: 'pointer' }}
                        >
                            <TableCell>{formatDateTime(run.started_at)}</TableCell>
                            <TableCell>{run.job_name}</TableCell>
                            <TableCell>
                                <Chip
                                    label={run.job_kind === 'oci' ? 'OCI' : '文件'}
                                    color={run.job_kind === 'oci' ? 'secondary' : 'default'}
                                    size="small"
                                    variant="outlined"
                                />
                            </TableCell>
                            <TableCell>
                                {run.trigger.charAt(0).toUpperCase() + run.trigger.slice(1)}
                                {run.scheduled_for !== undefined && (
                                    <Typography
                                        variant="caption"
                                        color="text.secondary"
                                        sx={{ display: 'block' }}
                                    >
                                        计划于 {formatDateTime(run.scheduled_for)}
                                    </Typography>
                                )}
                            </TableCell>
                            <TableCell>
                                <RunRecordChip run={run} />
                            </TableCell>
                            <TableCell>{formatDuration(run.started_at, run.finished_at)}</TableCell>
                            <TableCell>{changesCell(run)}</TableCell>
                            <TableCell align="right">{bytesCell(run)}</TableCell>
                        </TableRow>
                    ))}
                </TableBody>
            </Table>
        </TableContainer>
    )
}
