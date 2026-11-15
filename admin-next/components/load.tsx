'use client';
import {useEffect,useState} from 'react';import {clientGet} from '@/lib/api';
export function useData<T>(path:string){const [data,setData]=useState<T|null>(null),[error,setError]=useState(''),[loading,setLoading]=useState(true);const refresh=()=>{setLoading(true);clientGet<T>(path).then(setData).catch(e=>setError(e.message)).finally(()=>setLoading(false))};useEffect(refresh,[path]);return {data,error,loading,refresh}}
export function Loading(){return <div className="notice">در حال دریافت داده‌های واقعی از سرور…</div>}
export function ErrorState({message,retry}:{message:string;retry:()=>void}){return <div className="error">{message} <button className="btn secondary" onClick={retry}>تلاش دوباره</button></div>}
