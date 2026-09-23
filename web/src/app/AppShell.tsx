import DarkModeOutlinedIcon from '@mui/icons-material/DarkModeOutlined'
import DashboardOutlinedIcon from '@mui/icons-material/DashboardOutlined'
import FolderOutlinedIcon from '@mui/icons-material/FolderOutlined'
import HistoryOutlinedIcon from '@mui/icons-material/HistoryOutlined'
import KeyOutlinedIcon from '@mui/icons-material/KeyOutlined'
import LightModeOutlinedIcon from '@mui/icons-material/LightModeOutlined'
import LogoutOutlinedIcon from '@mui/icons-material/LogoutOutlined'
import MenuIcon from '@mui/icons-material/Menu'
import MenuOpenIcon from '@mui/icons-material/MenuOpen'
import SettingsBrightnessOutlinedIcon from '@mui/icons-material/SettingsBrightnessOutlined'
import SettingsOutlinedIcon from '@mui/icons-material/SettingsOutlined'
import ShareOutlinedIcon from '@mui/icons-material/ShareOutlined'
import StorageOutlinedIcon from '@mui/icons-material/StorageOutlined'
import SyncOutlinedIcon from '@mui/icons-material/SyncOutlined'
import {
    AppBar,
    Avatar,
    Box,
    Divider,
    Drawer,
    IconButton,
    List,
    ListItemButton,
    ListItemText,
    Menu,
    MenuItem,
    Toolbar,
    Tooltip,
    Typography,
    useMediaQuery,
    useTheme,
} from '@mui/material'
import { useState } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../features/auth/AuthProvider'
import { type ThemePreference, useColorMode } from './providers'

const expandedWidth = 240
const collapsedWidth = 72
const sidebarIconColumnWidth = 44
const collapseKey = 'tinysync-sidebar-collapsed'
const items = [
    { to: '/', label: '概览', icon: <DashboardOutlinedIcon /> },
    { to: '/sources', label: '同步源', icon: <StorageOutlinedIcon /> },
    { to: '/credentials', label: '凭据', icon: <KeyOutlinedIcon /> },
    { to: '/jobs', label: '同步任务', icon: <SyncOutlinedIcon /> },
    { to: '/files', label: '文件管理', icon: <FolderOutlinedIcon /> },
    { to: '/shares', label: '共享管理', icon: <ShareOutlinedIcon /> },
    { to: '/history', label: '运行历史', icon: <HistoryOutlinedIcon /> },
]
function ThemeIcon({ preference }: { preference: ThemePreference }) {
    if (preference === 'light') return <LightModeOutlinedIcon fontSize="small" />
    if (preference === 'dark') return <DarkModeOutlinedIcon fontSize="small" />
    return <SettingsBrightnessOutlinedIcon fontSize="small" />
}
export default function AppShell() {
    const theme = useTheme()
    const desktop = useMediaQuery(theme.breakpoints.up('md'))
    const location = useLocation()
    const navigate = useNavigate()
    const auth = useAuth()
    const { preference, setPreference } = useColorMode()
    const [mobileOpen, setMobileOpen] = useState(false)
    const [menuAnchor, setMenuAnchor] = useState<HTMLElement | null>(null)
    const [collapsed, setCollapsed] = useState(
        () => window.localStorage.getItem(collapseKey) === '1',
    )
    const width = collapsed ? collapsedWidth : expandedWidth
    const drawerTransition = theme.transitions.create('width', {
        duration: theme.transitions.duration.standard,
        easing: theme.transitions.easing.easeInOut,
    })
    const labelTransition = theme.transitions.create(['max-width', 'opacity'], {
        duration: theme.transitions.duration.standard,
        easing: theme.transitions.easing.easeInOut,
    })
    const toggle = () =>
        setCollapsed(value => {
            const next = !value
            window.localStorage.setItem(collapseKey, next ? '1' : '0')
            return next
        })
    const go = (to: string) => {
        navigate(to)
        setMobileOpen(false)
    }
    const signOut = async () => {
        setMenuAnchor(null)
        await auth.logout()
        navigate('/login', { replace: true })
    }
    const labelSx = (compact: boolean) => ({
        maxWidth: compact ? 0 : 180,
        opacity: compact ? 0 : 1,
        overflow: 'hidden',
        whiteSpace: 'nowrap',
        transition: labelTransition,
    })
    const iconColumnSx = {
        width: sidebarIconColumnWidth,
        flexShrink: 0,
        display: 'flex',
        justifyContent: 'center',
    } as const
    const sidebar = ({ compact, permanent }: { compact: boolean; permanent: boolean }) => (
        <Box sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
            {permanent && (
                <>
                    <Toolbar disableGutters sx={{ minHeight: 64, width: '100%' }}>
                        <Box
                            sx={{
                                width: collapsedWidth,
                                flexShrink: 0,
                                display: 'flex',
                                justifyContent: 'center',
                            }}
                        >
                            <Tooltip title={compact ? '展开侧边栏' : '折叠侧边栏'}>
                                <IconButton
                                    onClick={toggle}
                                    aria-label={compact ? '展开侧边栏' : '折叠侧边栏'}
                                    aria-pressed={compact}
                                >
                                    {compact ? (
                                        <MenuIcon fontSize="small" />
                                    ) : (
                                        <MenuOpenIcon fontSize="small" />
                                    )}
                                </IconButton>
                            </Tooltip>
                        </Box>
                    </Toolbar>
                    <Divider />
                </>
            )}
            <List sx={{ flex: 1, pt: permanent ? 1 : 2, px: 1.5 }}>
                {items.map(item => (
                    <Tooltip key={item.to} title={compact ? item.label : ''} placement="right">
                        <ListItemButton
                            selected={
                                item.to === '/'
                                    ? location.pathname === '/'
                                    : location.pathname.startsWith(item.to)
                            }
                            onClick={() => go(item.to)}
                            sx={{
                                minHeight: 48,
                                borderRadius: 1.5,
                                mb: 0.5,
                                px: 0,
                                overflow: 'hidden',
                            }}
                        >
                            <Box sx={iconColumnSx}>{item.icon}</Box>
                            <Box sx={labelSx(compact)}>
                                <ListItemText primary={item.label} sx={{ m: 0 }} />
                            </Box>
                        </ListItemButton>
                    </Tooltip>
                ))}
            </List>
            <Box sx={{ mt: 'auto', p: 1.5 }}>
                <Tooltip title={compact ? '设置' : ''} placement="right">
                    <ListItemButton
                        selected={location.pathname.startsWith('/settings')}
                        onClick={() => go('/settings')}
                        sx={{
                            minHeight: 48,
                            borderRadius: 1.5,
                            px: 0,
                            overflow: 'hidden',
                        }}
                    >
                        <Box sx={iconColumnSx}>
                            <SettingsOutlinedIcon />
                        </Box>
                        <Box sx={labelSx(compact)}>
                            <ListItemText primary="设置" sx={{ m: 0 }} />
                        </Box>
                    </ListItemButton>
                </Tooltip>
            </Box>
        </Box>
    )
    const title = location.pathname.startsWith('/sources')
        ? '同步源'
        : location.pathname.startsWith('/credentials')
          ? '凭据'
          : location.pathname.startsWith('/jobs')
            ? '同步任务'
            : location.pathname.startsWith('/files')
              ? '文件管理'
              : location.pathname.startsWith('/shares')
                ? '共享管理'
                : location.pathname.startsWith('/history')
                  ? '运行历史'
                  : location.pathname.startsWith('/settings')
                    ? '设置'
                    : '概览'
    return (
        <Box sx={{ display: 'flex', minHeight: '100vh', bgcolor: 'background.default' }}>
            <AppBar
                position="fixed"
                color="transparent"
                elevation={0}
                sx={{
                    width: desktop ? `calc(100% - ${width}px)` : '100%',
                    ml: desktop ? `${width}px` : 0,
                    borderBottom: 1,
                    borderColor: 'divider',
                    transition: drawerTransition,
                }}
            >
                <Toolbar>
                    <IconButton
                        onClick={() => setMobileOpen(true)}
                        sx={{ display: { md: 'none' }, mr: 1 }}
                    >
                        <MenuIcon />
                    </IconButton>
                    <Typography variant="h6" sx={{ fontWeight: 700, flex: 1 }}>
                        {title}
                    </Typography>
                    <IconButton onClick={event => setMenuAnchor(event.currentTarget)}>
                        <Avatar
                            src={auth.avatar || undefined}
                            sx={{ width: 32, height: 32, bgcolor: 'primary.main' }}
                        >
                            A
                        </Avatar>
                    </IconButton>
                </Toolbar>
            </AppBar>
            <Box
                component="nav"
                sx={{ width: desktop ? width : 0, flexShrink: 0, transition: drawerTransition }}
            >
                <Drawer
                    variant="temporary"
                    open={mobileOpen}
                    onClose={() => setMobileOpen(false)}
                    sx={{ display: { md: 'none' }, '& .MuiDrawer-paper': { width: expandedWidth } }}
                >
                    {sidebar({ compact: false, permanent: false })}
                </Drawer>
                <Drawer
                    variant="permanent"
                    sx={{
                        display: { xs: 'none', md: 'block' },
                        '& .MuiDrawer-paper': {
                            width,
                            boxSizing: 'border-box',
                            overflowX: 'hidden',
                            transition: drawerTransition,
                        },
                    }}
                >
                    {sidebar({ compact: collapsed, permanent: true })}
                </Drawer>
            </Box>
            <Box
                component="main"
                sx={{ flexGrow: 1, minWidth: 0, pt: 8, transition: drawerTransition }}
            >
                <Box sx={{ maxWidth: 1440, mx: 'auto', p: { xs: 2, md: 3 } }}>
                    <Outlet />
                </Box>
            </Box>
            <Menu
                anchorEl={menuAnchor}
                open={Boolean(menuAnchor)}
                onClose={() => setMenuAnchor(null)}
            >
                <MenuItem
                    onClick={() => {
                        setMenuAnchor(null)
                        go('/settings')
                    }}
                >
                    <SettingsOutlinedIcon fontSize="small" sx={{ mr: 1.5 }} />
                    用户设置
                </MenuItem>
                <MenuItem
                    onClick={() =>
                        setPreference(
                            preference === 'system'
                                ? 'light'
                                : preference === 'light'
                                  ? 'dark'
                                  : 'system',
                        )
                    }
                >
                    <ThemeIcon preference={preference} />
                    <Typography sx={{ ml: 1.5 }}>
                        {preference === 'system'
                            ? '自动主题'
                            : preference === 'light'
                              ? '亮色主题'
                              : '暗色主题'}
                    </Typography>
                </MenuItem>
                <MenuItem onClick={() => void signOut()}>
                    <LogoutOutlinedIcon fontSize="small" sx={{ mr: 1.5 }} />
                    登出
                </MenuItem>
            </Menu>
        </Box>
    )
}
