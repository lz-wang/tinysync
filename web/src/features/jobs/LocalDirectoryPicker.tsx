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
    Popover,
    TextField,
    Tooltip,
    Typography,
} from '@mui/material'
import { useCallback, useEffect, useState } from 'react'
import { createLocalDirectory, listLocalDirectoriesWithOptions } from '../../api'

// LocalDirectoryPicker 浏览运行 TinySync 的主机目录；只列出可进入的目录，
// 选择当前目录后把 canonical 路径交回 Job 表单。
export default function LocalDirectoryPicker({
    open,
    initialPath,
    onClose,
    onPick,
}: {
    open: boolean
    initialPath: string
    onClose: () => void
    onPick: (path: string) => void
}) {
    const [path, setPath] = useState('')
    const [directories, setDirectories] = useState<Array<{ path: string }>>([])
    const [loading, setLoading] = useState(false)
    const [error, setError] = useState<string | null>(null)
    const [showHidden, setShowHidden] = useState(false)
    const [folderAnchor, setFolderAnchor] = useState<HTMLElement | null>(null)
    const [folderName, setFolderName] = useState('')
    const [creatingFolder, setCreatingFolder] = useState(false)

    const browse = useCallback((target: string, includeHidden: boolean) => {
        setLoading(true)
        setError(null)
        return listLocalDirectoriesWithOptions(target, includeHidden)
            .then(response => {
                setPath(response.path)
                setDirectories(response.directories)
            })
            .catch(reason => setError(reason instanceof Error ? reason.message : String(reason)))
            .finally(() => setLoading(false))
    }, [])

    useEffect(() => {
        if (!open) {
            return
        }
        setShowHidden(false)
        void browse(initialPath.trim(), false)
    }, [browse, initialPath, open])

    const parentPath = path === '' ? '' : path.replace(/[\\/][^\\/]+$/, '') || path

    const createFolder = async () => {
        if (path === '' || folderName.trim() === '') {
            return
        }
        setCreatingFolder(true)
        setError(null)
        try {
            const created = await createLocalDirectory(path, folderName)
            setFolderAnchor(null)
            setFolderName('')
            await browse(created.path, showHidden)
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
                    选择本地目录
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
                                if (path !== '') {
                                    void browse(path, checked)
                                }
                            }}
                        />
                    }
                    label={<Typography variant="body2">显示隐藏文件</Typography>}
                />
                <Tooltip title="新建文件夹">
                    <span>
                        <IconButton
                            aria-label="新建文件夹"
                            disabled={loading || path === ''}
                            onClick={event => setFolderAnchor(event.currentTarget)}
                        >
                            <CreateNewFolderOutlinedIcon />
                        </IconButton>
                    </span>
                </Tooltip>
            </DialogTitle>
            <DialogContent
                dividers
                sx={{ minHeight: 0, display: 'flex', flexDirection: 'column', py: 1 }}
            >
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1, flexShrink: 0 }}>
                    {path || '正在读取目录…'}
                </Typography>
                {error !== null && <Alert severity="error">{error}</Alert>}
                {loading ? (
                    <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
                        <CircularProgress size={28} aria-label="正在读取目录" />
                    </Box>
                ) : (
                    <List disablePadding sx={{ flex: 1, overflowY: 'auto' }}>
                        {parentPath !== '' && parentPath !== path && (
                            <ListItemButton onClick={() => void browse(parentPath, showHidden)}>
                                <ListItemIcon>
                                    <ArrowUpwardOutlinedIcon />
                                </ListItemIcon>
                                <ListItemText primary="上级目录" />
                            </ListItemButton>
                        )}
                        {directories.map(directory => (
                            <ListItemButton
                                key={directory.path}
                                onClick={() => void browse(directory.path, showHidden)}
                            >
                                <ListItemIcon>
                                    <FolderOutlinedIcon color="primary" />
                                </ListItemIcon>
                                <ListItemText
                                    primary={directory.path.split(/[\\/]/).filter(Boolean).at(-1)}
                                />
                            </ListItemButton>
                        ))}
                    </List>
                )}
            </DialogContent>
            <DialogActions>
                <Button onClick={onClose}>取消</Button>
                <Button
                    variant="contained"
                    disabled={loading || path === ''}
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
