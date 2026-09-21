import {
    Box,
    Card,
    CardContent,
    CircularProgress,
    Pagination,
    Stack,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TableRow,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import { Link as RouterLink } from 'react-router-dom'
import type { RunRecordResponse } from '../api'
import { listRuns } from '../api'
import { useToast } from '../app/toast'
import { usePageTitle } from '../app/usePageTitle'
import {
    formatBytes,
    formatDateTime,
    formatDuration,
    RunStatusChip,
} from '../features/history/shared'

// pageSize 是历史列表每页条数（与服务端上限一致的默认值）。
const pageSize = 50

// HistoryPage 提供全局同步历史：所有 Job 最近的成功 / 失败 / 跳过
// 一览，分页浏览，点击行进入运行详情。
export default function HistoryPage() {
    usePageTitle('运行历史')
    const toast = useToast()
    const [runs, setRuns] = useState<RunRecordResponse[] | null>(null)
    const [total, setTotal] = useState(0)
    const [page, setPage] = useState(1)
    const [error, setError] = useState<string | null>(null)

    const load = useCallback(async (targetPage: number) => {
        const data = await listRuns({ limit: pageSize, offset: (targetPage - 1) * pageSize })
        setRuns(data.runs)
        setTotal(data.total)
    }, [])

    useEffect(() => {
        let cancelled = false
        setError(null)
        load(page).catch(e => {
            if (!cancelled) {
                const message = e instanceof Error ? e.message : String(e)
                setError(message)
                toast.error(message)
            }
        })
        return () => {
            cancelled = true
        }
    }, [load, page, toast])

    return (
        <Card variant="outlined">
            <CardContent>
                <Stack spacing={2}>
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
                            运行历史加载失败。
                        </Typography>
                    )}
                </Stack>
            </CardContent>
        </Card>
    )
}

function RunsTable({ runs }: { runs: RunRecordResponse[] }) {
    return (
        <TableContainer>
            <Table size="small">
                <TableHead>
                    <TableRow>
                        <TableCell>时间</TableCell>
                        <TableCell>任务</TableCell>
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
                                <RunStatusChip state={run.status} />
                            </TableCell>
                            <TableCell>{formatDuration(run.started_at, run.finished_at)}</TableCell>
                            <TableCell>
                                {`${run.stats.files_created}+ ${run.stats.files_updated}~ ${run.stats.files_deleted}- ${run.stats.files_skipped}↷`}
                            </TableCell>
                            <TableCell align="right">
                                {formatBytes(run.stats.bytes_transferred)}
                            </TableCell>
                        </TableRow>
                    ))}
                </TableBody>
            </Table>
        </TableContainer>
    )
}
