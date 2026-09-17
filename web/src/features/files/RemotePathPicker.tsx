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

// RemotePathPicker 是远端目录选择对话框：浏览目录树，确认当前目录
// 路径。绑定 sourceId 时（Job 配置场景）namespace 由调用方决定——
// Job.Source 决定远端身份，Picker 只负责在该 Source 下选目录，不
// 显示 Source 选择器；缺省时保留独立选择。
export default function RemotePathPicker({
    open,
    onClose,
    onPick,
    boundSourceId,
    initialPath,
}: {
    open: boolean
    onClose: () => void
    onPick: (path: string) => void
    // 绑定的 Source；提供时隐藏 Source 选择器且不再拉取 Source 列表。
    boundSourceId?: string
    // 打开时的初始目录；绑定模式下调用方回传 Job 的 Remote Root。
    initialPath?: string
}) {
    const [sources, setSources] = useState<SourceResponse[]>([])
    const [pickedSourceId, setPickedSourceId] = useState('')
    const [currentPath, setCurrentPath] = useState(initialPath ?? '/')
    const activeSourceId = boundSourceId ?? pickedSourceId

    useEffect(() => {
        if (!open || boundSourceId !== undefined) {
            return
        }
        let cancelled = false
        listSources()
            .then(list => {
                if (cancelled) {
                    return
                }
                setSources(list)
                setPickedSourceId(previous =>
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
    }, [open, boundSourceId])

    // 打开或初始路径变化时回到初始目录；Source 切换触发的根目录重
    // 载由 FileBrowser 经 onPathChange('/') 同步回这里。
    useEffect(() => {
        if (open) {
            setCurrentPath(initialPath ?? '/')
        }
    }, [open, initialPath])

    const load = useCallback(
        async (path: string, cursor: string | null) => {
            const page = await listRemoteFiles(activeSourceId, path, PAGE_SIZE, cursor ?? undefined)
            return { entries: page.entries, nextCursor: page.next_cursor }
        },
        [activeSourceId],
    )

    const confirm = () => {
        onPick(currentPath)
        onClose()
    }

    return (
        <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
            <DialogTitle>Browse remote directories</DialogTitle>
            <DialogContent>
                {boundSourceId === undefined && (
                    <TextField
                        select
                        size="small"
                        label="Source"
                        value={pickedSourceId}
                        onChange={event => setPickedSourceId(event.target.value)}
                        sx={{ minWidth: 260, mb: 2 }}
                    >
                        {sources.map(source => (
                            <MenuItem key={source.id} value={source.id}>
                                {source.name}（{source.type}）
                            </MenuItem>
                        ))}
                    </TextField>
                )}
                {activeSourceId !== '' ? (
                    <FileBrowser
                        load={load}
                        downloadURL={() => '#'}
                        onPathChange={setCurrentPath}
                        height={320}
                    />
                ) : (
                    <Typography variant="body2" color="text.secondary">
                        {boundSourceId !== undefined
                            ? '当前 Job 尚未选择 Source；请先选择 Source 再浏览。'
                            : '尚未创建 Source；请先在 Sources 页面添加。'}
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
                <Button onClick={confirm} variant="contained" disabled={activeSourceId === ''}>
                    Use this directory
                </Button>
            </DialogActions>
        </Dialog>
    )
}
