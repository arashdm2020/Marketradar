import {NextRequest} from 'next/server';
const upstream=process.env.GO_ADMIN_URL||'http://127.0.0.1:8088';

function firstForwarded(value:string|null){return value?.split(',')[0]?.trim()||''}
function sameOriginRequest(request:NextRequest){
 const raw=request.headers.get('origin');
 if(!raw)return false;
 let origin:URL;
 try{origin=new URL(raw)}catch{return false}
 if(origin.username||origin.password||origin.pathname!=='/'||origin.search||origin.hash)return false;
 const hosts=new Set([
  request.nextUrl.host,
  request.headers.get('host')||'',
  firstForwarded(request.headers.get('x-forwarded-host')),
 ].filter(Boolean).map(x=>x.toLowerCase()));
 if(!hosts.has(origin.host.toLowerCase()))return false;
 const proto=firstForwarded(request.headers.get('x-forwarded-proto'));
 if(proto&&origin.protocol!==proto.toLowerCase()+':')return false;
 const site=request.headers.get('sec-fetch-site');
 return !site||site==='same-origin';
}

async function forward(request:NextRequest,{params}:{params:Promise<{path:string[]}>}){
 const {path}=await params;const target=new URL('/api/'+path.join('/'),upstream);target.search=request.nextUrl.search;
 if(request.method!=='GET'){
  if(!sameOriginRequest(request))return new Response('origin rejected',{status:403});
 }
 const headers=new Headers();const cookie=request.headers.get('cookie');if(cookie)headers.set('cookie',cookie);
 if(request.method!=='GET'){headers.set('content-type',request.headers.get('content-type')||'application/x-www-form-urlencoded');headers.set('origin',new URL(upstream).origin)}
 try{const result=await fetch(target,{method:request.method,headers,body:request.method==='GET'?undefined:await request.text(),cache:'no-store',redirect:'manual',signal:AbortSignal.timeout(30000)});return new Response(result.body,{status:result.status,headers:{'content-type':result.headers.get('content-type')||'text/plain','cache-control':'no-store'}})}catch{return new Response('Go API unavailable',{status:503})}
}
export {forward as GET,forward as POST};
