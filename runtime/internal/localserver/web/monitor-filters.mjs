export function localDateValue(date) {
  return `${date.getFullYear()}-${String(date.getMonth()+1).padStart(2,'0')}-${String(date.getDate()).padStart(2,'0')}`;
}

// Calendar boundaries use the merchant's device timezone. Weeks begin Monday.
export function matchesMonitorDate(timestamp, period, selected, now=new Date()) {
  if(period==='all') return true;
  const date=new Date(timestamp*1000);
  if(Number.isNaN(date.getTime())) return false;
  if(period==='date') return Boolean(selected)&&localDateValue(date)===selected;
  const start=new Date(now.getFullYear(),now.getMonth(),now.getDate());
  const end=new Date(start);
  if(period==='week') {
    start.setDate(start.getDate()-(start.getDay()+6)%7);
    end.setTime(start.getTime());
    end.setDate(end.getDate()+7);
  } else end.setDate(end.getDate()+1);
  return date>=start&&date<end;
}
