import { Box, Button, Chip, CircularProgress, MenuItem, Stack, TextField } from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import {
    type FileEntry,
    type JobResponse,
    listJobs,
    listLocalFiles,
    localFileDownloadURL,
    runJob,
} from '../../api'
import FileBrowser, { PAGE_SIZE } from './FileBrowser'
import PublishDialog from './PublishDialog'

// managedBadge 展示 managed / unmanaged 标记：managed 为 TinySync
// 当前管理；unmanaged 为目录原有或已 relinquish 的文件。
function managedBadge(managed: boolean | undefined) {
    if (managed === undefined) {
        return null
    }
    return managed ? (
        <Chip size="small" color="primary" label="managed" variant="outlined" />
    ) : (
        <Chip size="small" label="unmanaged" variant="outlined" />
    )
}

// LocalFileBrowser 是本地文件浏览器：以 Job 为 namespace（唯一入口
// 是 Job.LocalRoot），展示 managed 标记，支持下载（Range / HEAD）、
// 一键触发同步与把 managed 文件加入发布。
export default function LocalFileBrowser({ onChanged }: { onChanged?: () => void }) {
    const [jobs, setJobs] = useState<JobResponse[] | null>(null)
    const [jobId, setJobId] = useState('')
    const [loadError, setLoadError] = useState<string | null>(null)
    const [publishTarget, setPublishTarget] = useState<FileEntry | null>(null)
    const [running, setRunning] = useState(false)
    const [actionError, setActionError] = useState<string | null>(null)

    useEffect(() => {
        let cancelled = false
        listJobs()
            .then(list => {
                if (cancelled) {
                    return
                }
                setJobs(list)
                if (list.length > 0) {
                    setJobId(previous => (previous === '' ? list[0].id : previous))
                }
            })
            .catch((err: unknown) => {
                if (!cancelled) {
                    setLoadError(err instanceof Error ? err.message : String(err))
                }
            })
        return () => {
            cancelled = true
        }
    }, [])

    const load = useCallback(
        async (path: string, cursor: string | null) => {
            const page = await listLocalFiles(jobId, path, PAGE_SIZE, cursor ?? undefined)
            return { entries: page.entries, nextCursor: page.next_cursor }
        },
        [jobId],
    )

    const triggerRun = async () => {
        setRunning(true)
        setActionError(null)
        try {
            await runJob(jobId)
        } catch (err) {
            setActionError(err instanceof Error ? err.message : String(err))
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
    if (loadError !== null) {
        return <Box sx={{ color: 'error.main' }}>加载 Job 失败：{loadError}</Box>
    }
    if (jobs !== null && jobs.length === 0) {
        return <Box sx={{ color: 'text.secondary' }}>尚未创建同步 Job；请先在 Jobs 页面添加。</Box>
    }

    return (
        <Box>
            <Stack direction="row" spacing={2} sx={{ mb: 2, alignItems: 'center' }}>
                <TextField
                    select
                    size="small"
                    label="Job"
                    value={jobId}
                    onChange={event => setJobId(event.target.value)}
                    sx={{ minWidth: 280 }}
                >
                    {jobs?.map(job => (
                        <MenuItem key={job.id} value={job.id}>
                            {job.name}
                        </MenuItem>
                    ))}
                </TextField>
                <Button
                    variant="outlined"
                    disabled={running || jobId === ''}
                    onClick={() => void triggerRun()}
                >
                    {running ? 'Triggering…' : 'Run sync now'}
                </Button>
            </Stack>
            {actionError !== null && <Box sx={{ color: 'error.main', mb: 1 }}>{actionError}</Box>}
            {jobId !== '' && (
                <FileBrowser
                    load={load}
                    downloadURL={path => localFileDownloadURL(jobId, path)}
                    renderEntryExtra={entry => (
                        <Stack direction="row" spacing={0.5} sx={{ alignItems: 'center' }}>
                            {managedBadge(entry.managed)}
                            {entry.kind === 'file' && entry.managed === true && (
                                <Button
                                    size="small"
                                    variant="text"
                                    onClick={() => setPublishTarget(entry)}
                                >
                                    Publish
                                </Button>
                            )}
                        </Stack>
                    )}
                />
            )}
            {publishTarget !== null && (
                <PublishDialog
                    open
                    jobId={jobId}
                    entryPath={publishTarget.path}
                    onClose={() => setPublishTarget(null)}
                    onCreated={() => onChanged?.()}
                />
            )}
        </Box>
    )
}
