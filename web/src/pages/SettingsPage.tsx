import NotificationsOutlinedIcon from '@mui/icons-material/NotificationsOutlined'
import { Stack, Tab, Tabs } from '@mui/material'
import { useLocation, useNavigate } from 'react-router-dom'
import { usePageTitle } from '../app/usePageTitle'
import AccountSettings from '../features/settings/AccountSettings'
import NotificationSettings from '../features/settings/NotificationSettings'
import TokensPage from './TokensPage'

// SettingsPage 是设置页的 tab / router shell：帐号、通知、开发者
//（API Token）三类。各 tab 的实现分别在 features/settings 与
// TokensPage，本组件只负责导航。
type SettingsTab = 'account' | 'notifications' | 'developer'

function tabFromPath(pathname: string): SettingsTab {
    if (pathname === '/settings/notifications') return 'notifications'
    if (pathname === '/settings/tokens') return 'developer'
    return 'account'
}

function pathFromTab(tab: SettingsTab): string {
    if (tab === 'notifications') return '/settings/notifications'
    if (tab === 'developer') return '/settings/tokens'
    return '/settings'
}

const TAB_TITLES: Record<SettingsTab, string> = {
    account: '帐号设置 · 设置',
    notifications: '通知 · 设置',
    developer: '开发者 · 设置',
}

export default function SettingsPage() {
    const location = useLocation()
    const navigate = useNavigate()
    const tab = tabFromPath(location.pathname)
    usePageTitle(TAB_TITLES[tab])
    return (
        <Stack spacing={3} sx={{ maxWidth: 900 }}>
            <Tabs
                value={tab}
                onChange={(_, value: SettingsTab) => navigate(pathFromTab(value))}
                aria-label="设置分类"
                sx={{ borderBottom: 1, borderColor: 'divider' }}
            >
                <Tab value="account" label="帐号设置" />
                <Tab
                    value="notifications"
                    icon={<NotificationsOutlinedIcon />}
                    iconPosition="start"
                    label="通知"
                />
                <Tab value="developer" label="开发者设置" />
            </Tabs>
            {tab === 'notifications' ? (
                <NotificationSettings />
            ) : tab === 'developer' ? (
                <TokensPage />
            ) : (
                <AccountSettings />
            )}
        </Stack>
    )
}
