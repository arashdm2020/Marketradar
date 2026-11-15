import {NextRequest,NextResponse} from 'next/server';
const go=process.env.GO_ADMIN_URL||'http://127.0.0.1:8088';
export async function POST(req:NextRequest){
 const origin=req.headers.get('origin'),host=req.headers.get('host');
 if(!origin||!host||new URL(origin).host!==host)return NextResponse.json({error:'origin rejected'},{status:403});
 const form=await req.formData(),code=String(form.get('code')||'').trim();
 if(!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(code))return NextResponse.json({error:'توکن یک‌بارمصرف معتبر نیست.'},{status:400});
 try{
  const upstream=await fetch(go+'/auth/code',{method:'POST',headers:{'content-type':'application/x-www-form-urlencoded','origin':new URL(go).origin},body:new URLSearchParams({code}),redirect:'manual',cache:'no-store',signal:AbortSignal.timeout(8000)});
  if(upstream.status!==303)return NextResponse.json({error:'کد ورود نامعتبر یا منقضی است.'},{status:401});
  const res=NextResponse.json({ok:true});const cookie=upstream.headers.get('set-cookie');if(cookie)res.headers.set('set-cookie',cookie);return res;
 }catch{return NextResponse.json({error:'سرویس ورود در دسترس نیست.'},{status:503})}
}
