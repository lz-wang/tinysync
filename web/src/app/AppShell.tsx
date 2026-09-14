import { AppBar, Box, Button, Container, Stack, Toolbar, Typography } from '@mui/material'
import { NavLink, Outlet } from 'react-router-dom'

// NavTab 是顶部导航项：激活态由 NavLink 判定。
function NavTab({ to, label }: { to: string; label: string }) {
    return (
        <NavLink to={to} style={{ textDecoration: 'none' }}>
            {({ isActive }) => (
                <Button size="small" variant={isActive ? 'contained' : 'text'}>
                    {label}
                </Button>
            )}
        </NavLink>
    )
}

// AppShell 是应用骨架：顶部导航 + 内容区。
export default function AppShell() {
    return (
        <Box sx={{ minHeight: '100vh', bgcolor: 'background.default' }}>
            <AppBar
                position="static"
                color="transparent"
                elevation={0}
                sx={{ borderBottom: 1, borderColor: 'divider' }}
            >
                <Toolbar>
                    <Typography variant="h6" component="div" sx={{ mr: 4, fontWeight: 600 }}>
                        TinySync
                    </Typography>
                    <Stack direction="row" spacing={1}>
                        <NavTab to="/" label="Overview" />
                        <NavTab to="/sources" label="Sources" />
                    </Stack>
                </Toolbar>
            </AppBar>
            <Container maxWidth="md" sx={{ py: 4 }}>
                <Outlet />
            </Container>
        </Box>
    )
}
