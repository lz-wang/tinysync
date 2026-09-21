import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type {
    JobResponse,
    RunProgressResponse,
    RunRecordResponse,
    RunStatusResponse,
    SourceResponse,
} from '../api'
import {
    cancelRun,
    createJob,
    deleteJob,
    fetchJobStatus,
    getRun,
    listJobs,
    listSources,
    runJob,
    updateJob,
} from '../api'
import { ToastProvider } from '../app/toast'
import JobsPage from './JobsPage'

// mock 整个 API 模块：页面树内 JobDialog / DeleteJobDialog 也引用它。
vi.mock('../api', () => ({
    cancelRun: vi.fn(),
    createJob: vi.fn(),
    deleteJob: vi.fn(),
    fetchJobStatus: vi.fn(),
    getRun: vi.fn(),
    listJobs: vi.fn(),
    listSources: vi.fn(),
    runJob: vi.fn(),
    updateJob: vi.fn(),
}))

const mocked = vi.mocked({
    cancelRun,
    createJob,
    deleteJob,
    fetchJobStatus,
    getRun,
    listJobs,
    listSources,
    runJob,
    updateJob,
})

const job: JobResponse = {
    id: 'job-1',
    name: '任务一',
    source_id: 'src-1',
    remote_root: '/',
    local_root: '/tmp/data',
    mode: 'copy',
    include: [],
    exclude: [],
    enabled: true,
    schedule: { type: 'manual' },
    created_at: '2026-09-22T00:00:00Z',
    updated_at: '2026-09-22T00:00:00Z',
}

const sources: SourceResponse[] = []

// statusFor 构造 /jobs/:id/status 响应；stats 为空统计。
function statusFor(state: RunStatusResponse['state'], runId?: string): RunStatusResponse {
    return {
        ...(runId !== undefined ? { run_id: runId } : {}),
        state,
        stats: {
            files_total: 0,
            files_created: 0,
            files_updated: 0,
            files_deleted: 0,
            files_skipped: 0,
            bytes_transferred: 0,
        },
    }
}

function progressOf(phase: RunProgressResponse['phase']): RunProgressResponse {
    return { phase, work_done: 1, work_total: 2 }
}

// recordFor 构造 /runs/:id 响应。
function recordFor(
    status: RunRecordResponse['status'],
    progress?: RunProgressResponse,
): RunRecordResponse {
    return {
        id: 'run-1',
        job_id: job.id,
        job_name: job.name,
        trigger: 'manual',
        status,
        started_at: '2026-09-22T00:00:00Z',
        stats: statusFor('running').stats,
        ...(progress !== undefined ? { progress } : {}),
    }
}

function renderPage() {
    return render(
        <MemoryRouter>
            <ToastProvider>
                <JobsPage />
            </ToastProvider>
        </MemoryRouter>,
    )
}

// flush 轮询定时器：推进一个 pollIntervalMs 并 flush 所有 promise 链。
async function tickPoll() {
    await act(async () => {
        await vi.advanceTimersByTimeAsync(1500)
    })
}

beforeEach(() => {
    // shouldAdvanceTime 让 waitFor / findBy 内部 timer 正常工作，
    // 同时保留对轮询 setInterval 的手动推进能力。
    vi.useFakeTimers({ shouldAdvanceTime: true })
    mocked.listJobs.mockResolvedValue([job])
    mocked.listSources.mockResolvedValue(sources)
    mocked.fetchJobStatus.mockResolvedValue(statusFor('succeeded'))
    mocked.getRun.mockResolvedValue(recordFor('running'))
})

afterEach(() => {
    // 未启用 vitest globals 时 RTL 不自动清理，手动卸载避免跨用例 DOM 残留。
    cleanup()
    vi.useRealTimers()
    vi.clearAllMocks()
})

describe('JobsPage 运行状态轮询', () => {
    it('手动运行后立即保存 run_id，停止按钮立即可发取消请求', async () => {
        mocked.runJob.mockResolvedValue({ run_id: 'run-1', state: 'running' })
        renderPage()
        fireEvent.click(await screen.findByLabelText('运行 任务一'))
        await waitFor(() => expect(screen.getByLabelText('停止 任务一')).toBeTruthy())
        fireEvent.click(screen.getByLabelText('停止 任务一'))
        await waitFor(() => expect(mocked.cancelRun).toHaveBeenCalledWith('run-1'))
    })

    it('轮询覆盖全部任务：调度在后台启动的 running 能被发现并拉取进度', async () => {
        renderPage()
        await waitFor(() => expect(screen.getByText('任务一')).toBeTruthy())
        // 页面加载后调度器才启动：status 变为 running 且带 run_id。
        mocked.fetchJobStatus.mockResolvedValue(statusFor('running', 'run-9'))
        await tickPoll()
        await waitFor(() => expect(screen.getByLabelText('停止 任务一')).toBeTruthy())
        expect(mocked.getRun).toHaveBeenCalledWith('run-9')
    })

    it('running 落终态后行内进度被清理', async () => {
        mocked.fetchJobStatus.mockResolvedValue(statusFor('running', 'run-1'))
        mocked.getRun.mockResolvedValue(recordFor('running', progressOf('transferring')))
        renderPage()
        // 等初始 reload 完成（jobsRef 已填充），轮询才会覆盖任务。
        await waitFor(() => expect(screen.getByText('任务一')).toBeTruthy())
        await tickPoll()
        await waitFor(() => expect(screen.getByLabelText('同步进度')).toBeTruthy())
        mocked.fetchJobStatus.mockResolvedValue(statusFor('succeeded'))
        await tickPoll()
        await waitFor(() => expect(screen.queryByLabelText('同步进度')).toBeNull())
    })

    it('transferring 且 work_total=0 时显示 100% 而非永久 indeterminate', async () => {
        mocked.fetchJobStatus.mockResolvedValue(statusFor('running', 'run-1'))
        mocked.getRun.mockResolvedValue(
            recordFor('running', { phase: 'transferring', work_done: 0, work_total: 0 }),
        )
        renderPage()
        await waitFor(() => expect(screen.getByText('任务一')).toBeTruthy())
        await tickPoll()
        const bar = await screen.findByLabelText('同步进度')
        await waitFor(() => expect(bar.getAttribute('aria-valuenow')).toBe('100'))
        expect(screen.getByText('100%')).toBeTruthy()
    })
})
