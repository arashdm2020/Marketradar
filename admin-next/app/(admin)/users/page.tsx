'use client';
import Link from 'next/link';
import { useState } from 'react';
import { ColumnDef } from '@tanstack/react-table';
import { Heading, Panel, Badge } from '@/components/ui';
import { DataTable } from '@/components/data-table';
import { ErrorState, Loading, useData } from '@/components/load';
import { clientPost } from '@/lib/api';
import { Users } from '@/lib/types';
import { ConfirmButton } from '@/components/confirm-button';

type Row = { key:string; id:number; channel:string; identity:string; searchText:string; displayName:string; name:string; username:string; phone:string; subscription:string; free:string; exempt:boolean; plan:string };

function telegramNames(user:Users['telegram'][number]) {
  const fullName=[user.first_name,user.last_name].filter(Boolean).join(' ').trim();
  const username=user.username?`@${user.username}`:'';
  const placeholder=`شناسهٔ تلگرام: ${user.id}`;
  return {
    displayName:user.display_name||fullName||user.provided_display_name||(username?`نام کاربری: ${username}`:placeholder),
    name:fullName||(username?`نام کاربری: ${username}`:placeholder),
  };
}

export default function UserPage() {
  const {data,error,loading,refresh}=useData<Users>('operations/users');
  const [message,setMessage]=useState('');
  const rows:Row[]=[
    ...(data?.telegram||[]).map(x=>({key:'telegram:'+x.id,id:x.id,channel:'تلگرام',identity:String(x.id),searchText:[x.id,x.first_name,x.last_name,x.display_name,x.provided_display_name,x.username,x.phone_number].filter(Boolean).join(' '),...telegramNames(x),username:x.username?'@'+x.username:'در دسترس نیست',phone:x.phone_number?`${x.phone_number} · ${x.phone_verified?'تأییدشده':'تأییدنشده'}`:'در دسترس نیست',subscription:x.subscription_active?'فعال':x.subscription_until?'منقضی':'بدون اشتراک',free:x.daily_allowance<0?'نامحدود':`${x.daily_remaining} از ${x.daily_allowance}`,exempt:x.exempt,plan:x.plan})),
    ...(data?.web||[]).map(x=>({key:'web:'+x.id,id:x.id,channel:'وب',identity:x.email,searchText:x.email,displayName:`ایمیل: ${x.email}`,name:'در دسترس نیست',username:'در دسترس نیست',phone:'در دسترس نیست',subscription:x.subscription_until&&Date.parse(x.subscription_until)>Date.now()?'فعال':x.subscription_until?'منقضی':'بدون اشتراک',free:'برای وب اعمال نمی‌شود',exempt:false,plan:x.plan||'visitor'})),
  ];
  async function toggle(row:Row){try{await clientPost('exemption',{user_id:String(row.id),exempt:String(!row.exempt)});setMessage('تغییر ثبت شد');refresh()}catch(e){setMessage((e as Error).message)}}
  const columns:ColumnDef<Row,unknown>[]=[
    {accessorKey:'identity',header:'شناسه',cell:x=><span className="mono">{x.row.original.identity}</span>},
    {accessorKey:'searchText',header:'نام برای جست‌وجو',cell:x=><span>{x.row.original.displayName}</span>},
    {accessorKey:'name',header:'نام'},
    {accessorKey:'username',header:'نام کاربری',cell:x=><span dir="ltr" className="mono">{x.row.original.username}</span>},
    {accessorKey:'phone',header:'تلفن',cell:x=><span dir="ltr" className="mono">{x.row.original.phone}</span>},
    {accessorKey:'channel',header:'کانال'},
    {accessorKey:'plan',header:'طرح',cell:x=><Badge value={x.row.original.plan}/>},
    {accessorKey:'subscription',header:'اشتراک',cell:x=><Badge value={x.row.original.subscription}/>},
    {accessorKey:'free',header:'سهمیهٔ امروز'},
    {accessorKey:'exempt',header:'معافیت',cell:x=>x.row.original.channel==='تلگرام'?<ConfirmButton label={x.row.original.exempt?'لغو معافیت':'فعال‌سازی معافیت'} title="تغییر دسترسی کاربر" description={`معافیت کاربر ${x.row.original.id} تغییر کند؟`} onConfirm={()=>toggle(x.row.original)}/>:'—'},
    {id:'detail',header:'پرونده',cell:x=><Link className="btn secondary" href={'/users/'+x.row.original.key}>مشاهده</Link>},
  ];
  return <><Heading title="مدیریت کاربران" description="جست‌وجو بر اساس شناسه، نام، نام کاربری یا شمارهٔ تلفن؛ شماره فقط در پنل ادمین نمایش داده می‌شود."/>{message&&<div className="notice" role="status">{message}</div>}<Panel title="فهرست کاربران">{loading?<Loading/>:error?<ErrorState message={error} retry={refresh}/>:<DataTable data={rows} columns={columns} search="شناسه، نام، نام کاربری، تلفن یا ایمیل" exportName="users"/>}</Panel></>;
}
