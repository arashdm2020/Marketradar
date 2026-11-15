import {NextRequest} from 'next/server';
const upstream=process.env.GO_ADMIN_URL||'http://127.0.0.1:8088';
async function forward(request:NextRequest,{params}:{params:Promise<{path:string[]}>}){
 const {path}=await params;const target=new URL('/api/'+path.join('/'),upstream);target.search=request.nextUrl.search;
 if(request.method!=='GET'){
  const origin=request.headers.get('origin'),site=request.headers.get('sec-fetch-site');
  if(!origin||new URL(origin).host!==request.nextUrl.host||(site&&site!=='same-origin'))return new Response('origin rejected',{status:403});
 }
 const headers=new Headers();const cookie=request.headers.get('cookie');if(cookie)headers.set('cookie',cookie);
 if(request.method!=='GET'){headers.set('content-type',request.headers.get('content-type')||'application/x-www-form-urlencoded');headers.set('origin',new URL(upstream).origin)}
 try{const result=await fetch(target,{method:request.method,headers,body:request.method==='GET'?undefined:await request.text(),cache:'no-store',redirect:'manual',signal:AbortSignal.timeout(30000)});return new Response(result.body,{status:result.status,headers:{'content-type':result.headers.get('content-type')||'text/plain','cache-control':'no-store'}})}catch{return new Response('Go API unavailable',{status:503})}
}
export {forward as GET,forward as POST};
