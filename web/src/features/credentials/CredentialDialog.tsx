import {
    Alert,
    Button,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    Stack,
    TextField,
    Typography,
} from '@mui/material'
import { useEffect, useState } from 'react'
import { type CredentialResponse, createCredential, updateCredential } from '../../api'

interface CredentialDialogProps {
    open: boolean
    // 编辑时传入现有凭据；创建时为 null。
    credential: CredentialResponse | null
    onClose: () => void
    onSaved: (credential: CredentialResponse) => void
}

// CredentialDialog 创建 / 编辑凭据。编辑语义与后端一致：私钥与口令
// 缺省保留，出现即整体替换（无三态）；secret 永不回显，重贴才替换。
export default function CredentialDialog({
    open,
    credential,
    onClose,
    onSaved,
}: CredentialDialogProps) {
    const [name, setName] = useState('')
    const [privateKey, setPrivateKey] = useState('')
    const [passphrase, setPassphrase] = useState('')
    const [saving, setSaving] = useState(false)
    const [error, setError] = useState<string | null>(null)

    useEffect(() => {
        if (!open) {
            return
        }
        setName(credential?.name ?? '')
        setPrivateKey('')
        setPassphrase('')
        setSaving(false)
        setError(null)
    }, [open, credential])

    // 创建必须提供私钥；编辑时重贴才替换（口令随之整体替换）。
    const canSave = name.trim() !== '' && (credential !== null || privateKey.trim() !== '')

    async function handleSave() {
        setSaving(true)
        setError(null)
        try {
            if (credential === null) {
                const created = await createCredential({
                    name,
                    type: 'ssh_key',
                    secret: {
                        private_key: privateKey,
                        ...(passphrase !== '' ? { private_key_passphrase: passphrase } : {}),
                    },
                })
                onSaved(created)
                return
            }
            const patch: {
                name?: string
                secret?: { private_key: string; private_key_passphrase?: string }
            } = {}
            if (name !== credential.name) {
                patch.name = name
            }
            if (privateKey.trim() !== '') {
                patch.secret = {
                    private_key: privateKey,
                    ...(passphrase !== '' ? { private_key_passphrase: passphrase } : {}),
                }
            }
            const updated = await updateCredential(credential.id, patch)
            onSaved(updated)
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e))
        } finally {
            setSaving(false)
        }
    }

    return (
        <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth disableRestoreFocus>
            <DialogTitle>{credential === null ? '创建凭据' : '编辑凭据'}</DialogTitle>
            <DialogContent>
                <Stack spacing={2} sx={{ pt: 1 }}>
                    {error !== null && <Alert severity="error">{error}</Alert>}
                    <TextField
                        label="名称"
                        value={name}
                        onChange={e => setName(e.target.value)}
                        required
                        autoFocus
                    />
                    <TextField
                        label="私钥（OpenSSH PEM）"
                        value={privateKey}
                        onChange={e => setPrivateKey(e.target.value)}
                        multiline
                        rows={8}
                        required={credential === null}
                        placeholder={
                            credential === null
                                ? '-----BEGIN OPENSSH PRIVATE KEY-----'
                                : '留空保留当前私钥；粘贴新钥即整体替换'
                        }
                        sx={{ '& textarea': { fontFamily: 'monospace' } }}
                    />
                    <TextField
                        label="私钥口令（可选）"
                        type="password"
                        value={passphrase}
                        onChange={e => setPassphrase(e.target.value)}
                        placeholder={credential === null ? '未加密钥匙留空' : '仅重贴私钥时需要'}
                    />
                    <Typography variant="caption" color="text.secondary">
                        私钥与口令作为整体保存，保存后不再回显；公钥指纹用于区分钥匙。
                        凭据引用与源内联私钥互斥。
                    </Typography>
                </Stack>
            </DialogContent>
            <DialogActions>
                <Button onClick={onClose} disabled={saving}>
                    取消
                </Button>
                <Button
                    variant="contained"
                    disabled={saving || !canSave}
                    onClick={() => void handleSave()}
                >
                    {saving ? '保存中…' : '保存'}
                </Button>
            </DialogActions>
        </Dialog>
    )
}
