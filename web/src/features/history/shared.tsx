import { Box, Chip } from '@mui/material'
import Typography from '@mui/material/Typography'
import type {
    JobKind,
    OciRunItemDetail,
    OciRunResultResponse,
    RunItemAction,
    RunItemStatus,
    RunResultPayload,
    RunState,
} from '../../api'
import { isOciRunResult } from '../../api'

// formatBytes 把字节数转为人类可读摘要。
export function formatBytes(bytes: number): string {
    if (bytes < 1024) {
        return `${bytes} B`
    }
    const units = ['KB', 'MB', 'GB', 'TB']
    let value = bytes
    let unit = 'B'
    for (const next of units) {
        if (value < 1024) {
            break
        }
        value /= 1024
        unit = next
    }
    return `${value.toFixed(1)} ${unit}`
}

// formatDateTime 把 RFC3339 时间渲染为本地可读形式；空值渲染为占位符。
export function formatDateTime(value?: string, placeholder = '—'): string {
    if (value === undefined || value === '') {
        return placeholder
    }
    const d = new Date(value)
    if (Number.isNaN(d.getTime())) {
        return value
    }
    return d.toLocaleString()
}

// formatDuration 把起止时间差渲染为紧凑时长。
export function formatDuration(startedAt: string, finishedAt?: string): string {
    const start = new Date(startedAt).getTime()
    const end = finishedAt === undefined ? Number.NaN : new Date(finishedAt).getTime()
    if (Number.isNaN(start) || Number.isNaN(end)) {
        return '—'
    }
    const seconds = Math.max(0, Math.round((end - start) / 1000))
    if (seconds < 60) {
        return `${seconds}s`
    }
    const minutes = Math.floor(seconds / 60)
    return `${minutes}m ${String(seconds % 60).padStart(2, '0')}s`
}

// runStateChipProps 把运行状态映射为 Chip 文案与颜色。
export function runStateChipProps(state: RunState): {
    label: string
    color: 'success' | 'error' | 'info' | 'warning' | 'default'
} {
    switch (state) {
        case 'succeeded':
            return { label: '成功', color: 'success' }
        case 'failed':
            return { label: '失败', color: 'error' }
        case 'running':
            return { label: '运行中', color: 'info' }
        case 'skipped':
            return { label: '已跳过', color: 'warning' }
        case 'canceled':
            return { label: '已取消', color: 'default' }
        default:
            return { label: '空闲', color: 'default' }
    }
}

// RunStatusChip 是运行状态徽标。
export function RunStatusChip({ state }: { state: RunState }) {
    const props = runStateChipProps(state)
    return <Chip label={props.label} color={props.color} size="small" />
}

// ociRunChipProps 按 R36 从 result_json 推导 oci run 的历史状态徽标：
// success>0 且 failure>0 → 部分成功；全 failure → 失败；全 skip → 跳过；
// 其余（running、无结果或全成功）按 RunState 渲染。RunState 枚举不扩。
export function ociRunChipProps(
    state: RunState,
    result: OciRunResultResponse | undefined,
): { label: string; color: 'success' | 'error' | 'info' | 'warning' | 'default' } {
    const success = result?.success_count ?? 0
    const failure = result?.failure_count ?? 0
    const skipped = result?.skipped_count ?? 0
    if (success > 0 && failure > 0) {
        return { label: '部分成功', color: 'warning' }
    }
    if (success === 0 && failure > 0) {
        return { label: '失败', color: 'error' }
    }
    if (success === 0 && failure === 0 && skipped > 0) {
        return { label: '跳过', color: 'warning' }
    }
    return runStateChipProps(state)
}

// RunRecordChip 是运行记录徽标：oci run 按 result_json 推导（部分成功
// 等领域语义），其余按 RunState 渲染。
export function RunRecordChip({
    run,
}: {
    run: { job_kind: JobKind; status: RunState; result: RunResultPayload }
}) {
    const props =
        run.job_kind === 'oci' && isOciRunResult(run.result)
            ? ociRunChipProps(run.status, run.result)
            : runStateChipProps(run.status)
    return <Chip label={props.label} color={props.color} size="small" />
}

// itemStatusColor 把明细状态映射为时间线圆点颜色。canceled 用中性灰：
// 被用户停止中断不是失败（红），也不同于跳过（浅灰淡显）的语义。
export function itemStatusColor(status: RunItemStatus): string {
    switch (status) {
        case 'succeeded':
            return 'success.main'
        case 'failed':
            return 'error.main'
        case 'canceled':
            return 'text.secondary'
        default:
            return 'text.disabled'
    }
}

// RunItemLine 是时间线式明细行：状态圆点 + 目标 + 动作 + 字节 + 错误。
// detail 为 oci 明细的领域扩展（目标 Registry / 平台级结果），原样
// 透传展示；file 领域明细无该字段。
export function RunItemLine({
    path,
    action,
    status,
    bytes,
    error,
    detail,
}: {
    path: string
    action: RunItemAction
    status: RunItemStatus
    bytes: number
    error?: string
    detail?: OciRunItemDetail
}) {
    const platforms = detail?.platform_results ?? []
    return (
        <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 1 }}>
            <Box
                component="span"
                sx={{
                    width: 8,
                    height: 8,
                    borderRadius: '50%',
                    flexShrink: 0,
                    alignSelf: 'center',
                    bgcolor: itemStatusColor(status),
                }}
            />
            <Typography
                variant="body2"
                component="code"
                sx={{ fontFamily: 'monospace', wordBreak: 'break-all' }}
            >
                {path}
            </Typography>
            <Typography variant="caption" color="text.secondary">
                {action} · {status}
                {bytes > 0 ? ` · ${formatBytes(bytes)}` : ''}
                {detail?.registry ? ` · ${detail.registry}` : ''}
            </Typography>
            {platforms.length > 0 && (
                <Typography
                    variant="caption"
                    color="text.secondary"
                    sx={{ wordBreak: 'break-word' }}
                >
                    {platforms.map(item => `${item.platform}:${item.status}`).join(' · ')}
                </Typography>
            )}
            {error !== undefined && error !== '' && (
                <Typography
                    variant="caption"
                    color={status === 'failed' ? 'error' : 'text.secondary'}
                    sx={{ wordBreak: 'break-word' }}
                >
                    {error}
                </Typography>
            )}
        </Box>
    )
}
