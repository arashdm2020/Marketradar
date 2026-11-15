import {NextRequest,NextResponse} from 'next/server';
export async function POST(req:NextRequest){if(req.headers.get('origin')!==req.nextUrl.origin)return new Response('origin rejected',{status:403});const r=NextResponse.redirect(new URL('/login',req.url),303);r.cookies.set('mr_admin','',{path:'/',maxAge:0,httpOnly:true,sameSite:'strict'});return r}
