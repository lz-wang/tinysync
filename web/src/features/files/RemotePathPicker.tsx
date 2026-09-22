import ArrowUpwardOutlinedIcon from '@mui/icons-material/ArrowUpwardOutlined'
import CreateNewFolderOutlinedIcon from '@mui/icons-material/CreateNewFolderOutlined'
import FolderOutlinedIcon from '@mui/icons-material/FolderOutlined'
import {
    Alert,
    Box,
    Button,
    Checkbox,
    CircularProgress,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControlLabel,
    IconButton,
    List,
    ListItemButton,
    ListItemIcon,
    ListItemText,
    MenuItem,
    Popover,
    TextField,
    Tooltip,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import { createRemoteDirectory, listRemoteFiles, listSources, type SourceResponse } from '../../api'

// RemotePathPicker 与本地目录选择器采用同一尺寸与交互：只展示目录，默认
// 隐藏点开头的条目，并可在当前远端目录创建直接子目录。readOnly 用于
// 只读协议（GitHub Release 等）：隐藏新建文件夹入口，浏览仍可用。
export default function RemotePathPicker({
    open,
    onClose,
    onPick,
    boundSourceId,
    initialPath,
    readOnly,
}: {
    open: boolean
    onClose: () => void
    onPick: (path: string) => void
    boundSourceId?: string
    initialPath?: string
    readOnly?: boolean
}) {
    const [sources, setSources] = useState<SourceResponse[]>([])
    const [pickedSourceId, setPickedSourceId] = useState('')
    const [path, setPath] = useState(initialPath ?? '/')
    const [directories, setDirectories] = useState<Array<{ path: string; name: string }>>([])
    const [loading, setLoading] = useState(false)
    const [error, setError] = useState<string | null>(null)
    const [showHidden, setShowHidden] = useState(false)
    const [folderAnchor, setFolderAnchor] = useState<HTMLElement | null>(null)
    const [folderName, setFolderName] = useState('')
    const [creatingFolder, setCreatingFolder] = useState(false)
    const activeSourceId = boundSourceId ?? pickedSourceId

    const browse = useCallback(async (sourceId: string, target: string, includeHidden: boolean) => {
        if (sourceId === '') return
        setLoading(true)
        setError(null)
        try {
            const page = await listRemoteFiles(sourceId, target, 500, undefined, includeHidden)
            setPath(page.path)
            setDirectories(
                page.entries
                    .filter(entry => entry.kind === 'directory')
                    .map(entry => ({ path: entry.path, name: entry.name })),
            )
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason))
        } finally {
            setLoading(false)
        }
    }, [])

    useEffect(() => {
        if (!open || boundSourceId !== undefined) return
        let cancelled = false
        listSources()
            .then(list => {
                if (!cancelled) {
                    setSources(list)
                    setPickedSourceId(previous =>
                        previous === '' && list.length > 0 ? list[0].id : previous,
                    )
                }
            })
            .catch(() => {
                if (!cancelled) setSources([])
            })
        return () => {
            cancelled = true
        }
    }, [boundSourceId, open])

    useEffect(() => {
        if (!open || activeSourceId === '') return
        setShowHidden(false)
        void browse(activeSourceId, initialPath ?? '/', false)
    }, [activeSourceId, browse, initialPath, open])

    const parentPath = path === '/' ? '/' : path.slice(0, path.lastIndexOf('/')) || '/'
    const createFolder = async () => {
        if (activeSourceId === '' || folderName.trim() === '') return
        setCreatingFolder(true)
        setError(null)
        try {
            const created = await createRemoteDirectory(activeSourceId, path, folderName)
            setFolderAnchor(null)
            setFolderName('')
            await browse(activeSourceId, created.path, showHidden)
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : String(reason))
        } finally {
            setCreatingFolder(false)
        }
    }

    return (
        <Dialog
            open={open}
            onClose={onClose}
            maxWidth={false}
            slotProps={{ paper: { sx: { width: '50vw', maxWidth: 'none', height: '80vh' } } }}
        >
            <DialogTitle sx={{ display: 'flex', alignItems: 'center', gap: 2, py: 1 }}>
                <Typography component="span" variant="h6" sx={{ flexGrow: 1 }}>
                    选择远端目录
                </Typography>
                <FormControlLabel
                    sx={{ mr: 0 }}
                    control={
                        <Checkbox
                            size="small"
                            checked={showHidden}
                            onChange={event => {
                                const checked = event.target.checked
                                setShowHidden(checked)
                                void browse(activeSourceId, path, checked)
                            }}
                        />
                    }
                    label={<Typography variant="body2">显示隐藏文件</Typography>}
                />
                {!readOnly && (
                    <Tooltip title="新建文件夹">
                        <span>
                            <IconButton
                                aria-label="新建文件夹"
                                disabled={loading || activeSourceId === ''}
                                onClick={event => setFolderAnchor(event.currentTarget)}
                            >
                                <CreateNewFolderOutlinedIcon />
                            </IconButton>
                        </span>
                    </Tooltip>
                )}
            </DialogTitle>
            <DialogContent
                dividers
                sx={{ minHeight: 0, display: 'flex', flexDirection: 'column', py: 1 }}
            >
                {boundSourceId === undefined && (
                    <TextField
                        select
                        size="small"
                        label="同步源"
                        value={pickedSourceId}
                        onChange={event => setPickedSourceId(event.target.value)}
                        sx={{ minWidth: 260, mb: 1 }}
                    >
                        {sources.map(source => (
                            <MenuItem key={source.id} value={source.id}>
                                {source.name}（{source.type}）
                            </MenuItem>
                        ))}
                    </TextField>
                )}
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1, flexShrink: 0 }}>
                    {activeSourceId === '' ? '请先选择同步源。' : path}
                </Typography>
                {error !== null && <Alert severity="error">{error}</Alert>}
                {activeSourceId !== '' &&
                    (loading ? (
                        <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
                            <CircularProgress size={28} aria-label="正在读取目录" />
                        </Box>
                    ) : (
                        <List disablePadding sx={{ flex: 1, overflowY: 'auto' }}>
                            {path !== '/' && (
                                <ListItemButton
                                    onClick={() =>
                                        void browse(activeSourceId, parentPath, showHidden)
                                    }
                                >
                                    <ListItemIcon>
                                        <ArrowUpwardOutlinedIcon />
                                    </ListItemIcon>
                                    <ListItemText primary="上级目录" />
                                </ListItemButton>
                            )}
                            {directories.map(directory => (
                                <ListItemButton
                                    key={directory.path}
                                    onClick={() =>
                                        void browse(activeSourceId, directory.path, showHidden)
                                    }
                                >
                                    <ListItemIcon>
                                        <FolderOutlinedIcon color="primary" />
                                    </ListItemIcon>
                                    <ListItemText primary={directory.name} />
                                </ListItemButton>
                            ))}
                        </List>
                    ))}
            </DialogContent>
            <DialogActions>
                <Button onClick={onClose}>取消</Button>
                <Button
                    variant="contained"
                    disabled={loading || activeSourceId === ''}
                    onClick={() => onPick(path)}
                >
                    使用此目录
                </Button>
            </DialogActions>
            <Popover
                open={folderAnchor !== null}
                anchorEl={folderAnchor}
                onClose={() => {
                    setFolderAnchor(null)
                    setFolderName('')
                }}
                anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
                transformOrigin={{ vertical: 'top', horizontal: 'right' }}
            >
                <Box
                    component="form"
                    onSubmit={event => {
                        event.preventDefault()
                        void createFolder()
                    }}
                    sx={{ display: 'flex', gap: 1, p: 1.5 }}
                >
                    <TextField
                        autoFocus
                        size="small"
                        label="文件夹名称"
                        value={folderName}
                        onChange={event => setFolderName(event.target.value)}
                    />
                    <Button
                        type="submit"
                        variant="contained"
                        disabled={creatingFolder || folderName.trim() === ''}
                    >
                        新建
                    </Button>
                </Box>
            </Popover>
        </Dialog>
    )
}
