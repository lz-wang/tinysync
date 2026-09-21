import {
    Alert,
    Box,
    Button,
    Card,
    CardContent,
    CircularProgress,
    Divider,
    Stack,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { Link as RouterLink, useParams } from 'react-router-dom'
import { getRun, listRunItems, type RunItemResponse, type RunRecordResponse } from '../api'
import { useToast } from '../app/toast'
import { usePageTitle } from '../app/usePageTitle'
import {
    formatBytes,
    formatDateTime,
    formatDuration,
    RunItemLine,
    RunStatusChip,
} from '../features/history/shared'

// RunDetailPage 展示单轮运行的完整信息：摘要统计与文件变化时间线
//（时间线式历史视图，行内圆点颜色区分 succeeded / failed / skipped）。
export default function RunDetailPage() {
    const { runId } = useParams<{ runId: string }>()
    const toast = useToast()
    const [run, setRun] = useState<RunRecordResponse | null>(null)
    usePageTitle(run && run.id === runId ? `${run.job_name} · 运行详情` : '运行详情')
    const [items, setItems] = useState<RunItemResponse[]>([])
    const [itemTotal, setItemTotal] = useState(0)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        let cancelled = false
        async function load() {
            if (runId === undefined) {
                throw new Error('missing run id')
            }
            const record = await getRun(runId)
            const detail = await listRunItems(runId)
            if (cancelled) {
                return
            }
            setRun(record)
            setItems(detail.items)
            setItemTotal(detail.total)
        }
        setError(null)
        setRun(null)
        load().catch(e => {
            if (!cancelled) {
                const message = e instanceof Error ? e.message : String(e)
                setError(message)
                toast.error(message)
            }
        })
        return () => {
            cancelled = true
        }
    }, [runId, toast])

    return (
        <Stack spacing={2}>
            <Button
                component={RouterLink}
                to="/history"
                size="small"
                sx={{ alignSelf: 'flex-start' }}
            >
                ← 返回运行历史
            </Button>
            {run === null && error === null ? (
                <CircularProgress size={24} aria-label="加载中" />
            ) : run !== null ? (
                <>
                    <Card variant="outlined">
                        <CardContent>
                            <Stack spacing={1.5}>
                                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5 }}>
                                    <Typography variant="h5" component="h1">
                                        {run.job_name}
                                    </Typography>
                                    <RunStatusChip state={run.status} />
                                </Box>
                                <SummaryGrid
                                    entries={[
                                        [
                                            '触发方式',
                                            run.trigger === 'manual' ? '手动' : run.trigger,
                                        ],
                                        ['计划时间', formatDateTime(run.scheduled_for, '—')],
                                        ['开始时间', formatDateTime(run.started_at, '—')],
                                        ['结束时间', formatDateTime(run.finished_at, '—')],
                                        ['耗时', formatDuration(run.started_at, run.finished_at)],
                                        [
                                            '文件',
                                            `共 ${run.stats.files_total} · 新增 ${run.stats.files_created} · 更新 ${run.stats.files_updated} · 删除 ${run.stats.files_deleted} · 跳过 ${run.stats.files_skipped}`,
                                        ],
                                        ['传输量', formatBytes(run.stats.bytes_transferred)],
                                    ]}
                                />
                                {run.error !== undefined && run.error !== '' && (
                                    <Alert
                                        severity={run.status === 'skipped' ? 'warning' : 'error'}
                                    >
                                        {run.error}
                                    </Alert>
                                )}
                            </Stack>
                        </CardContent>
                    </Card>
                    <Card variant="outlined">
                        <CardContent>
                            <Stack spacing={1.5}>
                                <Typography variant="caption" color="text.secondary">
                                    共记录 {itemTotal} 个文件变更（未变化的文件不显示）
                                </Typography>
                                <Divider />
                                {items.length === 0 ? (
                                    <Typography variant="body2" color="text.secondary">
                                        本次运行没有文件变更。
                                    </Typography>
                                ) : (
                                    <Stack spacing={1}>
                                        {items.map(item => (
                                            <RunItemLine
                                                key={item.id}
                                                path={item.path}
                                                action={item.action}
                                                status={item.status}
                                                bytes={item.bytes}
                                                error={item.error}
                                            />
                                        ))}
                                    </Stack>
                                )}
                            </Stack>
                        </CardContent>
                    </Card>
                </>
            ) : (
                // 初始加载失败：详情已在 toast 中展示，区域保留简短失败文案。
                <Typography variant="body2" color="text.secondary">
                    运行详情加载失败。
                </Typography>
            )}
        </Stack>
    )
}

// SummaryGrid 是两列摘要：标签列固定宽度，值列可换行。
function SummaryGrid({ entries }: { entries: [string, string][] }) {
    return (
        <Box
            sx={{
                display: 'grid',
                gridTemplateColumns: 'max-content 1fr',
                columnGap: 3,
                rowGap: 0.75,
                alignItems: 'baseline',
            }}
        >
            {entries.map(([label, value]) => (
                <Box key={label} sx={{ display: 'contents' }}>
                    <Typography variant="body2" color="text.secondary">
                        {label}
                    </Typography>
                    <Typography variant="body2">{value}</Typography>
                </Box>
            ))}
        </Box>
    )
}
