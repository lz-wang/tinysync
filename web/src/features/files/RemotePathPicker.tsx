import {
    Button,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    MenuItem,
    TextField,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import { listRemoteFiles, listSources, type SourceResponse } from '../../api'
import FileBrowser, { PAGE_SIZE } from './FileBrowser'

// RemotePathPicker 是远端目录选择对话框：选择 Source 后浏览目录树，
// 确认当前目录路径。remote select 是纯 UI 行为，不建立任何后端
// selection persistence。
export default function RemotePathPicker({
    open,
    onClose,
    onPick,
}: {
    open: boolean
    onClose: () => void
    onPick: (path: string) => void
}) {
    const [sources, setSources] = useState<SourceResponse[]>([])
    const [sourceId, setSourceId] = useState('')
    const [currentPath, setCurrentPath] = useState('/')

    useEffect(() => {
        if (!open) {
            return
        }
        let cancelled = false
        listSources()
            .then(list => {
                if (cancelled) {
                    return
                }
                setSources(list)
                setSourceId(previous =>
                    previous === '' && list.length > 0 ? list[0].id : previous,
                )
            })
            .catch(() => {
                if (!cancelled) {
                    setSources([])
                }
            })
        return () => {
            cancelled = true
        }
    }, [open])

    const load = useCallback(
        async (path: string, cursor: string | null) => {
            const page = await listRemoteFiles(sourceId, path, PAGE_SIZE, cursor ?? undefined)
            return { entries: page.entries, nextCursor: page.next_cursor }
        },
        [sourceId],
    )

    const confirm = () => {
        onPick(currentPath)
        onClose()
    }

    return (
        <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
            <DialogTitle>Browse remote directories</DialogTitle>
            <DialogContent>
                <TextField
                    select
                    size="small"
                    label="Source"
                    value={sourceId}
                    onChange={event => setSourceId(event.target.value)}
                    sx={{ minWidth: 260, mb: 2 }}
                >
                    {sources.map(source => (
                        <MenuItem key={source.id} value={source.id}>
                            {source.name}（{source.type}）
                        </MenuItem>
                    ))}
                </TextField>
                {sourceId !== '' ? (
                    <FileBrowser
                        load={load}
                        downloadURL={() => '#'}
                        onPathChange={setCurrentPath}
                        height={320}
                    />
                ) : (
                    <Typography variant="body2" color="text.secondary">
                        尚未创建 Source；请先在 Sources 页面添加。
                    </Typography>
                )}
            </DialogContent>
            <DialogActions>
                <Typography
                    variant="body2"
                    color="text.secondary"
                    sx={{ mr: 'auto', fontFamily: 'monospace' }}
                >
                    {currentPath}
                </Typography>
                <Button onClick={onClose}>Cancel</Button>
                <Button onClick={confirm} variant="contained" disabled={sourceId === ''}>
                    Use this directory
                </Button>
            </DialogActions>
        </Dialog>
    )
}
