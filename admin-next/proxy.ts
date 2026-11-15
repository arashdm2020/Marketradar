import {NextRequest,NextResponse} from 'next/server';
const go=process.env.GO_ADMIN_URL||'http://127.0.0.1:8088';
export async function proxy(req:NextRequest){
 const path=req.nextUrl.pathname;
 if(path.startsWith('/_next')||path==='/favicon.ico'||path.startsWith('/api/'))return NextResponse.next();
 let allowed=false;
 try { const r=await fetch(go+'/api/session',{headers:{cookie:req.headers.get('cookie')||''},cache:'no-store',signal:AbortSignal.timeout(3500)});allowed=r.ok; }catch{}
 if(path==='/login'){if(allowed)return NextResponse.redirect(new URL('/',req.url));return NextResponse.next()}
 if(!allowed)return NextResponse.redirect(new URL('/login',req.url));
 return NextResponse.next();
}
export const config={matcher:['/((?!_next/static|_next/image|favicon.ico).*)']};
