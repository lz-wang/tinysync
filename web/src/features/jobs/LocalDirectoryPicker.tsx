import ArrowUpwardOutlinedIcon from '@mui/icons-material/ArrowUpwardOutlined'
import FolderOutlinedIcon from '@mui/icons-material/FolderOutlined'
import {
    Alert,
    Box,
    Button,
    CircularProgress,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    List,
    ListItemButton,
    ListItemIcon,
    ListItemText,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { listLocalDirectories } from '../../api'

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

    useEffect(() => {
        if (!open) {
            return
        }
        const target = initialPath.trim()
        setLoading(true)
        setError(null)
        void listLocalDirectories(target)
            .then(response => {
                setPath(response.path)
                setDirectories(response.directories)
            })
            .catch(reason => setError(reason instanceof Error ? reason.message : String(reason)))
            .finally(() => setLoading(false))
    }, [open, initialPath])

    const browse = (target: string) => {
        setLoading(true)
        setError(null)
        void listLocalDirectories(target)
            .then(response => {
                setPath(response.path)
                setDirectories(response.directories)
            })
            .catch(reason => setError(reason instanceof Error ? reason.message : String(reason)))
            .finally(() => setLoading(false))
    }

    const parentPath = path === '' ? '' : path.replace(/[\\/][^\\/]+$/, '') || path

    return (
        <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
            <DialogTitle>选择本地目录</DialogTitle>
            <DialogContent dividers sx={{ minHeight: 360 }}>
                <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
                    {path || '正在读取目录…'}
                </Typography>
                {error !== null && <Alert severity="error">{error}</Alert>}
                {loading ? (
                    <Box sx={{ display: 'flex', justifyContent: 'center', py: 6 }}>
                        <CircularProgress size={28} aria-label="正在读取目录" />
                    </Box>
                ) : (
                    <List disablePadding>
                        {parentPath !== '' && parentPath !== path && (
                            <ListItemButton onClick={() => browse(parentPath)}>
                                <ListItemIcon>
                                    <ArrowUpwardOutlinedIcon />
                                </ListItemIcon>
                                <ListItemText primary="上级目录" />
                            </ListItemButton>
                        )}
                        {directories.map(directory => (
                            <ListItemButton
                                key={directory.path}
                                onClick={() => browse(directory.path)}
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
        </Dialog>
    )
}
