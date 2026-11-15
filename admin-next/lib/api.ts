export async function clientGet<T>(path:string):Promise<T>{const r=await fetch('/api/go/'+path,{cache:'no-store',signal:AbortSignal.timeout(10000)});if(!r.ok)throw new Error(r.status===403?'دسترسی مجاز نیست':'داده دریافت نشد');return r.json() as Promise<T>}
export async function clientPost<T>(path:string,data:Record<string,string>):Promise<T>{const r=await fetch('/api/go/'+path,{method:'POST',headers:{'content-type':'application/x-www-form-urlencoded'},body:new URLSearchParams(data)});if(!r.ok)throw new Error('ثبت عملیات ناموفق بود ('+r.status+')');return r.json() as Promise<T>}
export function bnb(wei:string|null|undefined){if(!wei)return 'در دسترس نیست';try{const n=BigInt(wei);const a=n/BigInt("1000000000000000000"),b=(n%BigInt("1000000000000000000")).toString().padStart(18,'0').replace(/0+$/,'');return a.toString()+(b?'.'+b:'')+' BNB'}catch{return 'در دسترس نیست'}}
export const show=(x:unknown)=>x===null||x===undefined||x===''?'در دسترس نیست':String(x);

