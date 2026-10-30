const form=document.getElementById('pair-form');
form.addEventListener('submit',async event=>{
 event.preventDefault();const button=form.querySelector('button'),input=document.getElementById('ticket'),status=document.getElementById('pair-status');button.disabled=true;
 try{const response=await fetch('/api/v1/kiosk/session',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({ticket:input.value.trim().toLowerCase()}),signal:AbortSignal.timeout(10000)});input.value='';
 if(response.ok){status.textContent='Screen paired. Returning to pickup.';location.replace('/');}
 else status.textContent=response.status===429?'Too many attempts. Wait before trying again.':'Pairing failed. Generate a fresh code with the local administrator helper.';
 }catch{status.textContent='Unable to confirm pairing. Check the local service and generate a fresh code.';}finally{button.disabled=false;}
});
