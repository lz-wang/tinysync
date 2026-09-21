import {
    Alert,
    Box,
    Button,
    Card,
    CardContent,
    CircularProgress,
    Divider,
    LinearProgress,
    Stack,
    Typography,
} from '@mui/material'
import { useEffect, useRef, useState } from 'react'
import { Link as RouterLink, useParams } from 'react-router-dom'
import {
    cancelRun,
    getRun,
    listRunItems,
    type RunItemResponse,
    type RunPhase,
    type RunProgressFileResponse,
    type RunProgressResponse,
    type RunRecordResponse,
} from '../api'
import { useToast } from '../app/toast'
import { usePageTitle } from '../app/usePageTitle'
import {
    formatBytes,
    formatDateTime,
    formatDuration,
    RunItemLine,
    RunStatusChip,
} from '../features/history/shared'

// pollIntervalMs 是运行中详情的轮询间隔：进度数字的粒度需求（HomeLab
// 场景、传输以秒计）下 1s 足够，无需长连接。
const pollIntervalMs = 1000

// phaseLabels 把运行阶段渲染为可读文案。
const phaseLabels: Record<RunPhase, string> = {
    connecting: '连接远端',
    scanning: '扫描远端',
    planning: '生成计划',
    transferring: '传输文件',
    finalizing: '收尾清理',
}

// RunDetailPage 展示单轮运行的完整信息：摘要统计与文件变化时间线
//（时间线式历史视图，行内圆点颜色区分 succeeded / failed / canceled）。
// 运行中的 run 以 1s 轮询刷新：indeterminate→determinate 总进度条、
// 在途文件字节进度与停止按钮。
export default function RunDetailPage() {
    const { runId } = useParams<{ runId: string }>()
    const toast = useToast()
    const [run, setRun] = useState<RunRecordResponse | null>(null)
    usePageTitle(run && run.id === runId ? `${run.job_name} · 运行详情` : '运行详情')
    const [items, setItems] = useState<RunItemResponse[]>([])
    const [itemTotal, setItemTotal] = useState(0)
    const [error, setError] = useState<string | null>(null)
    const [canceling, setCanceling] = useState(false)
    const running = run?.status === 'running'

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

    // 运行中轮询运行摘要（进度快照随响应更新）；终态后自动停止。
    useEffect(() => {
        if (!running || runId === undefined) {
            return
        }
        const timer = window.setInterval(() => {
            void getRun(runId)
                .then(record =>
                    setRun(prev => (prev !== null && prev.id === record.id ? record : prev)),
                )
                .catch(() => undefined)
        }, pollIntervalMs)
        return () => window.clearInterval(timer)
    }, [running, runId])

    // 运行刚落终态：文件明细补一次终态刷新（轮询只更新摘要）。
    const prevRunningRef = useRef(running)
    useEffect(() => {
        if (prevRunningRef.current && !running && runId !== undefined) {
            void listRunItems(runId)
                .then(detail => {
                    setItems(detail.items)
                    setItemTotal(detail.total)
                })
                .catch(() => undefined)
        }
        prevRunningRef.current = running
    }, [running, runId])

    const handleCancel = async () => {
        if (runId === undefined) {
            return
        }
        setCanceling(true)
        try {
            await cancelRun(runId)
            toast.info('停止请求已发送，等待运行结束…')
        } catch (e) {
            // 已终态（409）等确定性失败同样经 toast 提示。
            toast.error(e instanceof Error ? e.message : String(e))
        } finally {
            setCanceling(false)
        }
    }

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
                                    {running && (
                                        <Button
                                            size="small"
                                            color="error"
                                            variant="outlined"
                                            disabled={canceling}
                                            onClick={() => void handleCancel()}
                                        >
                                            停止
                                        </Button>
                                    )}
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
                                        severity={
                                            run.status === 'skipped' || run.status === 'canceled'
                                                ? 'warning'
                                                : 'error'
                                        }
                                    >
                                        {run.error}
                                    </Alert>
                                )}
                            </Stack>
                        </CardContent>
                    </Card>
                    {running && run.progress !== undefined && (
                        <RunProgressCard progress={run.progress} />
                    )}
                    <Card variant="outlined">
                        <CardContent>
                            <Stack spacing={1.5}>
                                <Typography variant="caption" color="text.secondary">
                                    共记录 {itemTotal} 个文件变更（未变化的文件不显示）
                                </Typography>
                                <Divider />
                                {items.length === 0 ? (
                                    <Typography variant="body2" color="text.secondary">
                                        {running
                                            ? '运行进行中，已完成的文件变更会陆续出现在这里。'
                                            : '本次运行没有文件变更。'}
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

// RunProgressCard 渲染运行中 run 的实时进度：总进度（工作量计数）与
// 在途文件字节进度。扫描 / 计划阶段工作量未知，以 indeterminate 表达，
// 不伪造百分比；transferring 起分母已确定。
function RunProgressCard({ progress }: { progress: RunProgressResponse }) {
    const known = progress.work_total > 0
    const percent = known ? Math.min(100, (progress.work_done / progress.work_total) * 100) : null
    const activeFiles = progress.active_files ?? []
    return (
        <Card variant="outlined">
            <CardContent>
                <Stack spacing={1.5}>
                    <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 1.5 }}>
                        <Typography variant="subtitle2">{phaseLabels[progress.phase]}</Typography>
                        {percent !== null && (
                            <Typography variant="caption" color="text.secondary">
                                {percent.toFixed(1)}% ({progress.work_done}/{progress.work_total})
                            </Typography>
                        )}
                    </Box>
                    {percent !== null ? (
                        <LinearProgress variant="determinate" value={percent} aria-label="总进度" />
                    ) : (
                        <LinearProgress variant="indeterminate" aria-label="总进度" />
                    )}
                    {activeFiles.length > 0 && <Divider />}
                    {activeFiles.map(file => (
                        <FileProgressLine key={file.path} file={file} />
                    ))}
                </Stack>
            </CardContent>
        </Card>
    )
}

// FileProgressLine 是单个在途文件：环形进度按字节占比推进，文字显示
// 原始计数「14.5 MB / 46.0 MB」（总大小未知时只显示已传输）。
function FileProgressLine({ file }: { file: RunProgressFileResponse }) {
    const known = file.bytes_total > 0
    const percent = known ? Math.min(100, (file.bytes_done / file.bytes_total) * 100) : undefined
    const label = known
        ? `${formatBytes(file.bytes_done)} / ${formatBytes(file.bytes_total)}`
        : formatBytes(file.bytes_done)
    return (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5 }}>
            <CircularProgress
                size={24}
                variant={percent !== undefined ? 'determinate' : 'indeterminate'}
                value={percent}
                aria-label={`传输进度 ${file.path}`}
            />
            <Typography
                variant="body2"
                component="code"
                sx={{ fontFamily: 'monospace', wordBreak: 'break-all', flex: 1 }}
            >
                {file.path}
            </Typography>
            <Typography variant="caption" color="text.secondary" sx={{ whiteSpace: 'nowrap' }}>
                {file.action} · {label}
            </Typography>
        </Box>
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
