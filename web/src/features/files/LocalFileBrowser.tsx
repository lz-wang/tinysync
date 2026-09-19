import SyncOutlinedIcon from '@mui/icons-material/SyncOutlined'
import { Alert, Box, Button, CircularProgress, IconButton, Tooltip } from '@mui/material'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
    type FileEntry,
    type JobResponse,
    listJobs,
    listLocalFiles,
    localFileDownloadURL,
    runJob,
} from '../../api'
import PublishDialog from './PublishDialog'
import ReadOnlyFileManager from './ReadOnlyFileManager'

// LocalFileBrowser 使用 Job.LocalRoot 作为唯一命名空间。Job 和路径都
// 来源于 URL，因此浏览位置可以被后退、刷新和分享链接准确恢复。
export default function LocalFileBrowser({ onChanged }: { onChanged?: () => void }) {
    const [jobs, setJobs] = useState<JobResponse[] | null>(null)
    const [loadError, setLoadError] = useState<string | null>(null)
    const [running, setRunning] = useState(false)
    const [actionError, setActionError] = useState<string | null>(null)
    const [publishTarget, setPublishTarget] = useState<FileEntry | null>(null)
    const [searchParams, setSearchParams] = useSearchParams()
    const jobId = searchParams.get('job') ?? ''
    const path = searchParams.get('path') ?? '/'

    useEffect(() => {
        let cancelled = false
        listJobs()
            .then(list => {
                if (!cancelled) setJobs(list)
            })
            .catch((error: unknown) => {
                if (!cancelled) setLoadError(error instanceof Error ? error.message : String(error))
            })
        return () => {
            cancelled = true
        }
    }, [])

    useEffect(() => {
        if (jobs === null || jobs.length === 0 || jobs.some(job => job.id === jobId)) return
        const next = new URLSearchParams(searchParams)
        next.set('job', jobs[0].id)
        next.set('path', '/')
        setSearchParams(next, { replace: true })
    }, [jobId, jobs, searchParams, setSearchParams])

    const resources = useMemo(
        () => (jobs ?? []).map(job => ({ id: job.id, label: job.name })),
        [jobs],
    )
    const updateLocation = (nextJobId: string, nextPath: string) => {
        const next = new URLSearchParams(searchParams)
        next.set('job', nextJobId)
        next.set('path', nextPath)
        setSearchParams(next)
    }
    const load = useCallback(
        async (targetPath: string, cursor?: string) => {
            const page = await listLocalFiles(jobId, targetPath, 100, cursor)
            return { entries: page.entries, nextCursor: page.next_cursor }
        },
        [jobId],
    )
    const triggerRun = async () => {
        setRunning(true)
        setActionError(null)
        try {
            await runJob(jobId)
        } catch (error) {
            setActionError(error instanceof Error ? error.message : String(error))
        } finally {
            setRunning(false)
        }
    }

    if (jobs === null && loadError === null) {
        return (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
                <CircularProgress />
            </Box>
        )
    }
    if (loadError !== null) return <Alert severity="error">加载同步任务失败：{loadError}</Alert>
    if (jobs?.length === 0)
        return <Alert severity="info">尚未创建同步任务；请先在“同步任务”页面添加。</Alert>

    return (
        <>
            {actionError !== null && (
                <Alert severity="error" sx={{ mb: 1 }}>
                    {actionError}
                </Alert>
            )}
            <ReadOnlyFileManager
                downloadURL={entryPath => localFileDownloadURL(jobId, entryPath)}
                load={load}
                onPathChange={nextPath => updateLocation(jobId, nextPath)}
                onResourceChange={nextJobId => updateLocation(nextJobId, '/')}
                path={path}
                renderActions={entry =>
                    entry.kind === 'file' && entry.managed === true ? (
                        <Button onClick={() => setPublishTarget(entry)} size="small">
                            发布
                        </Button>
                    ) : null
                }
                resourceId={jobId}
                resourceLabel="同步任务"
                resources={resources}
                showManaged
                toolbarActions={
                    <Tooltip title={running ? '正在触发同步…' : '立即同步'}>
                        <span>
                            <IconButton
                                aria-label="立即同步"
                                disabled={running || jobId === ''}
                                onClick={() => void triggerRun()}
                                size="small"
                            >
                                <SyncOutlinedIcon fontSize="small" />
                            </IconButton>
                        </span>
                    </Tooltip>
                }
            />
            {publishTarget !== null && (
                <PublishDialog
                    entryPath={publishTarget.path}
                    jobId={jobId}
                    onClose={() => setPublishTarget(null)}
                    onCreated={() => onChanged?.()}
                    open
                />
            )}
        </>
    )
}
