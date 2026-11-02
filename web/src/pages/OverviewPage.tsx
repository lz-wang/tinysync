import {
    Box,
    Card,
    CardContent,
    Chip,
    CircularProgress,
    Divider,
    Stack,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { Link as RouterLink } from 'react-router-dom'
import {
    fetchHealth,
    fetchVersion,
    type JobResponse,
    listJobs,
    listRuns,
    type RunRecordResponse,
} from '../api'
import { usePageTitle } from '../app/usePageTitle'
import { formatDateTime, RunRecordChip } from '../features/history/shared'

// OverviewData 是概览页的数据快照：服务健康 + 任务统计 + 近期执行
//（全部来自 /jobs 与 /runs 现成端点，不引入新接口）。
interface OverviewData {
    healthy: boolean | null
    version: string | null
    jobs: JobResponse[]
    running: RunRecordResponse[]
    recent: RunRecordResponse[]
    lastFailed: RunRecordResponse | null
    error: string | null
}

// OverviewPage 展示服务状态、任务与运行概览：任务总数 / 启用 / 运行中 /
// 最近失败 + 近期执行列表（克制版，参照 oci-sync 首页结构收敛）。
export default function OverviewPage() {
    usePageTitle('概览')
    const [data, setData] = useState<OverviewData | null>(null)

    useEffect(() => {
        let cancelled = false
        async function load() {
            const [health, version, jobs, running, recent, failedQuery] = await Promise.allSettled([
                fetchHealth(),
                fetchVersion(),
                listJobs(),
                listRuns({ status: 'running', limit: 50 }),
                listRuns({ limit: 8 }),
                // 最近失败单查一次：固定 limit 的近期窗口可能不含最新失败。
                listRuns({ status: 'failed', limit: 1 }),
            ])
            if (cancelled) {
                return
            }
            const failed =
                [health, version, jobs, running, recent, failedQuery].find(
                    result => result.status === 'rejected',
                ) ?? null
            setData({
                healthy: health.status === 'fulfilled' ? health.value.status === 'ok' : null,
                version: version.status === 'fulfilled' ? version.value.version : null,
                jobs: jobs.status === 'fulfilled' ? jobs.value : [],
                running: running.status === 'fulfilled' ? running.value.runs : [],
                recent: recent.status === 'fulfilled' ? recent.value.runs : [],
                lastFailed:
                    failedQuery.status === 'fulfilled' ? (failedQuery.value.runs[0] ?? null) : null,
                error:
                    failed !== null && failed.status === 'rejected'
                        ? String(
                              failed.reason instanceof Error
                                  ? failed.reason.message
                                  : failed.reason,
                          )
                        : null,
            })
        }
        load().catch(() => {
            if (!cancelled) {
                setData({
                    healthy: null,
                    version: null,
                    jobs: [],
                    running: [],
                    recent: [],
                    lastFailed: null,
                    error: '概览加载失败',
                })
            }
        })
        return () => {
            cancelled = true
        }
    }, [])

    return (
        <Stack spacing={2}>
            {data === null ? (
                <CircularProgress size={24} aria-label="加载中" />
            ) : (
                <>
                    {data.error !== null && (
                        <Typography variant="caption" color="error">
                            {data.error}
                        </Typography>
                    )}
                    <Card variant="outlined">
                        <CardContent>
                            <Stack spacing={2}>
                                <Row label="服务状态">
                                    {data.healthy === null ? (
                                        <Chip label="无法连接" color="error" size="small" />
                                    ) : (
                                        <Chip
                                            label={data.healthy ? '运行中' : '异常'}
                                            color={data.healthy ? 'success' : 'warning'}
                                            size="small"
                                        />
                                    )}
                                </Row>
                                <Row label="版本">
                                    <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
                                        {data.version ?? '-'}
                                    </Typography>
                                </Row>
                            </Stack>
                        </CardContent>
                    </Card>
                    <Card variant="outlined">
                        <CardContent>
                            <Stack
                                direction={{ xs: 'column', sm: 'row' }}
                                divider={<Divider orientation="vertical" flexItem />}
                                spacing={2}
                                sx={{ flexWrap: 'wrap' }}
                            >
                                <Stat label="任务总数" value={String(data.jobs.length)} />
                                <Stat
                                    label="已启用"
                                    value={String(data.jobs.filter(job => job.enabled).length)}
                                />
                                <Stat
                                    label="运行中"
                                    value={String(data.running.length)}
                                    highlight={data.running.length > 0 ? 'info' : undefined}
                                />
                                <Box sx={{ flex: 1, minWidth: 160 }}>
                                    <Typography variant="h5" sx={{ fontWeight: 700 }}>
                                        {data.lastFailed === null ? (
                                            <Typography
                                                component="span"
                                                variant="h5"
                                                color="text.secondary"
                                            >
                                                无
                                            </Typography>
                                        ) : (
                                            <Box
                                                component={RouterLink}
                                                to={`/history/${data.lastFailed.id}`}
                                                sx={{
                                                    color: 'error.main',
                                                    textDecoration: 'none',
                                                    '&:hover': { textDecoration: 'underline' },
                                                }}
                                            >
                                                {data.lastFailed.job_name}
                                            </Box>
                                        )}
                                    </Typography>
                                    <Typography variant="caption" color="text.secondary">
                                        最近失败
                                        {data.lastFailed !== null &&
                                            ` · ${formatDateTime(data.lastFailed.started_at)}`}
                                    </Typography>
                                </Box>
                            </Stack>
                        </CardContent>
                    </Card>
                    <Card variant="outlined">
                        <CardContent>
                            <Stack spacing={1.5}>
                                <Typography variant="subtitle2">近期执行</Typography>
                                <Divider />
                                {data.recent.length === 0 ? (
                                    <Typography variant="body2" color="text.secondary">
                                        暂无执行记录。手动运行任务或等待计划触发。
                                    </Typography>
                                ) : (
                                    data.recent.map(run => (
                                        <Stack
                                            key={run.id}
                                            direction="row"
                                            spacing={1.5}
                                            sx={{ alignItems: 'center', minWidth: 0 }}
                                        >
                                            <RunRecordChip run={run} />
                                            <Typography
                                                variant="body2"
                                                sx={{ minWidth: 0, overflowWrap: 'anywhere' }}
                                            >
                                                {run.job_name}
                                            </Typography>
                                            <Typography
                                                variant="caption"
                                                color="text.secondary"
                                                sx={{ whiteSpace: 'nowrap' }}
                                            >
                                                {run.job_kind === 'oci' ? 'OCI' : '文件'}
                                            </Typography>
                                            <Box sx={{ flexGrow: 1 }} />
                                            <Typography
                                                variant="caption"
                                                color="text.secondary"
                                                sx={{ whiteSpace: 'nowrap' }}
                                            >
                                                {formatDateTime(run.started_at)}
                                            </Typography>
                                        </Stack>
                                    ))
                                )}
                            </Stack>
                        </CardContent>
                    </Card>
                </>
            )}
        </Stack>
    )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
    return (
        <Box
            sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 2 }}
        >
            <Typography variant="body2" color="text.secondary">
                {label}
            </Typography>
            {children}
        </Box>
    )
}

// Stat 是单个统计块。
function Stat({ label, value, highlight }: { label: string; value: string; highlight?: 'info' }) {
    return (
        <Box sx={{ flex: 1, minWidth: 120 }}>
            <Typography
                variant="h5"
                sx={{
                    fontWeight: 700,
                    color: highlight === 'info' ? 'info.main' : 'text.primary',
                }}
            >
                {value}
            </Typography>
            <Typography variant="caption" color="text.secondary">
                {label}
            </Typography>
        </Box>
    )
}
