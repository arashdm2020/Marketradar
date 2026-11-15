export function Heading({title,description,actions}:{title:string;description:string;actions?:React.ReactNode}){return <div className="page-head"><div><h1>{title}</h1><p>{description}</p></div>{actions}</div>}
export function Stat({label,value,tone}:{label:string;value:string|number;tone?:string}){return <div className="stat"><small>{label}</small><strong style={tone?{color:`var(--${tone})`}:undefined}>{value}</strong></div>}
export function Panel({title,children}:{title:string;children:React.ReactNode}){return <section className="panel"><h2>{title}</h2>{children}</section>}
export function State({message}:{message:string}){return <div className="empty">{message}</div>}
export function Badge({value}:{value:string}){const tone=/paid|active|completed|delivered|transferred|موفق/.test(value)?'good':/fail|expired|error|late|ناموفق/.test(value)?'bad':'warn';return <span className={'badge '+tone}>{value||'نامشخص'}</span>}
