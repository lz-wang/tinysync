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
    const [run, setRun] = useState<RunRecordResponse | null>(null)
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
                setError(e instanceof Error ? e.message : String(e))
            }
        })
        return () => {
            cancelled = true
        }
    }, [runId])

    return (
        <Stack spacing={2}>
            <Button
                component={RouterLink}
                to="/history"
                size="small"
                sx={{ alignSelf: 'flex-start' }}
            >
                ← Back to History
            </Button>
            {error !== null && <Alert severity="error">{error}</Alert>}
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
                                            'Trigger',
                                            run.trigger.charAt(0).toUpperCase() +
                                                run.trigger.slice(1),
                                        ],
                                        ['Scheduled For', formatDateTime(run.scheduled_for, '—')],
                                        ['Started', formatDateTime(run.started_at, '—')],
                                        ['Finished', formatDateTime(run.finished_at, '—')],
                                        [
                                            'Duration',
                                            formatDuration(run.started_at, run.finished_at),
                                        ],
                                        [
                                            'Files',
                                            `${run.stats.files_total} total · ${run.stats.files_created} created · ${run.stats.files_updated} updated · ${run.stats.files_deleted} deleted · ${run.stats.files_skipped} skipped`,
                                        ],
                                        ['Bytes', formatBytes(run.stats.bytes_transferred)],
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
                                <Typography variant="h6" component="h2">
                                    Changes
                                </Typography>
                                <Typography variant="caption" color="text.secondary">
                                    {itemTotal} file change{itemTotal === 1 ? '' : 's'} recorded
                                    (unchanged files are not listed)
                                </Typography>
                                <Divider />
                                {items.length === 0 ? (
                                    <Typography variant="body2" color="text.secondary">
                                        No file changes in this run.
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
            ) : null}
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
