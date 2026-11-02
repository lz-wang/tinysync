import KeyOutlinedIcon from '@mui/icons-material/KeyOutlined'
import NotificationsOutlinedIcon from '@mui/icons-material/NotificationsOutlined'
import ShareOutlinedIcon from '@mui/icons-material/ShareOutlined'
import { Stack, Tab, Tabs } from '@mui/material'
import { useLocation, useNavigate } from 'react-router-dom'
import { usePageTitle } from '../app/usePageTitle'
import AccountSettings from '../features/settings/AccountSettings'
import MCPIntegrationPlaceholder from '../features/settings/MCPIntegrationPlaceholder'
import NotificationSettings from '../features/settings/NotificationSettings'
import CredentialsPage from './CredentialsPage'
import SharesPage from './SharesPage'
import TokensPage from './TokensPage'

// SettingsPage 是设置页的 tab / router shell：帐号、通知、凭据、共享
// 管理（R37：自一级导航降级迁入）、开发者（API Token）、MCP 集成（静态
// 占位）。各 tab 的实现分别在 features/* 与对应页面，本组件只负责导航。
type SettingsTab = 'account' | 'notifications' | 'credentials' | 'shares' | 'developer' | 'mcp'

function tabFromPath(pathname: string): SettingsTab {
    if (pathname === '/settings/notifications') return 'notifications'
    if (pathname === '/settings/credentials') return 'credentials'
    if (pathname === '/settings/shares') return 'shares'
    if (pathname === '/settings/tokens') return 'developer'
    if (pathname === '/settings/mcp') return 'mcp'
    return 'account'
}

function pathFromTab(tab: SettingsTab): string {
    if (tab === 'notifications') return '/settings/notifications'
    if (tab === 'credentials') return '/settings/credentials'
    if (tab === 'shares') return '/settings/shares'
    if (tab === 'developer') return '/settings/tokens'
    if (tab === 'mcp') return '/settings/mcp'
    return '/settings'
}

const TAB_TITLES: Record<SettingsTab, string> = {
    account: '帐号设置 · 设置',
    notifications: '通知 · 设置',
    credentials: '凭据 · 设置',
    shares: '共享管理 · 设置',
    developer: '开发者 · 设置',
    mcp: 'MCP 集成 · 设置',
}

export default function SettingsPage() {
    const location = useLocation()
    const navigate = useNavigate()
    const tab = tabFromPath(location.pathname)
    usePageTitle(TAB_TITLES[tab])
    return (
        <Stack spacing={3} sx={{ maxWidth: 1080 }}>
            <Tabs
                value={tab}
                onChange={(_, value: SettingsTab) => navigate(pathFromTab(value))}
                aria-label="设置分类"
                variant="scrollable"
                scrollButtons="auto"
                sx={{ borderBottom: 1, borderColor: 'divider' }}
            >
                <Tab value="account" label="帐号设置" />
                <Tab
                    value="notifications"
                    icon={<NotificationsOutlinedIcon />}
                    iconPosition="start"
                    label="通知"
                />
                <Tab
                    value="credentials"
                    icon={<KeyOutlinedIcon />}
                    iconPosition="start"
                    label="凭据"
                />
                <Tab
                    value="shares"
                    icon={<ShareOutlinedIcon />}
                    iconPosition="start"
                    label="共享管理"
                />
                <Tab value="developer" label="开发者设置" />
                <Tab value="mcp" label="MCP 集成" />
            </Tabs>
            {tab === 'notifications' ? (
                <NotificationSettings />
            ) : tab === 'credentials' ? (
                <CredentialsPage />
            ) : tab === 'shares' ? (
                <SharesPage />
            ) : tab === 'developer' ? (
                <TokensPage />
            ) : tab === 'mcp' ? (
                <MCPIntegrationPlaceholder />
            ) : (
                <AccountSettings />
            )}
        </Stack>
    )
}
