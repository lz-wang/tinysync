import { useEffect } from 'react'

export function usePageTitle(title: string) {
    useEffect(() => {
        document.title = `${title} · TinySync`
        return () => {
            document.title = 'TinySync'
        }
    }, [title])
}
