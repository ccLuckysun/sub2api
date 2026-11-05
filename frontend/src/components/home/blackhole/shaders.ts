// Extracted from our independently written makeRelativisticRenderer in the approved
// docs-local/style-previews/kerrfield-blackhole.html. No reference-site modules.
export const vertexShader = `#version 300 es
precision highp float;
void main(){vec2 p=vec2(float((gl_VertexID<<1)&2),float(gl_VertexID&2));gl_Position=vec4(p*2.-1.,0.,1.);}`

export const fragmentShader = `#version 300 es
precision highp float;
uniform vec2 uResolution;
uniform vec2 uPointer;
uniform vec2 uCenter;
uniform float uTime;
uniform float uDeep;
uniform float uIntro;
uniform int uSteps;
out vec4 outColor;
const float PI=3.14159265359;
float hash(vec2 p){p=fract(p*vec2(123.34,456.21));p+=dot(p,p+45.32);return fract(p.x*p.y);}
float noise(vec2 p){vec2 i=floor(p),f=fract(p);f=f*f*(3.-2.*f);return mix(mix(hash(i),hash(i+vec2(1,0)),f.x),mix(hash(i+vec2(0,1)),hash(i+1.),f.x),f.y);}
float turbulence(vec2 p){float n=0.;n+=noise(p)*.54;p=mat2(1.7,1.2,-1.2,1.7)*p;n+=noise(p)*.28;p=mat2(1.7,1.2,-1.2,1.7)*p;n+=noise(p)*.14;return n;}
vec3 heat(float t){vec3 c=mix(vec3(.22,.055,.018),vec3(1.,.38,.13),smoothstep(0.,.42,t));c=mix(c,vec3(1.,.79,.52),smoothstep(.24,.72,t));return mix(c,vec3(.9,.88,1.),smoothstep(.65,1.1,t));}
vec3 disk(vec3 p,vec3 ray,float mass){
 float r=length(p.xz);float inner=3.25*mass,outer=12.;if(r<inner||r>outer)return vec3(0.);
 float a=atan(p.z,p.x),w=5.4/pow(max(r,1.),1.4),spin=a-uTime*w*(1.+uDeep*.9);
 float n=turbulence(vec2(cos(spin)*3.5,sin(spin)*3.5)+r*.44);
 float bands=.45+.55*pow(.5+.5*sin(r*26.+n*7.+sin(spin*6.)*.8),2.);
 float fine=.6+.4*noise(vec2(r*71.,spin*11.));
 float envelope=smoothstep(inner,inner+.85,r)*(1.-smoothstep(8.3,outer,r));
 float radial=pow(inner/max(r,inner),1.5);
 vec3 velocity=normalize(vec3(-p.z,0.,p.x));float doppler=1./max(.47,1.-dot(velocity,-ray)*sqrt(mass/r)*.88);
 float temp=(.23+radial*.73)*pow(doppler,.6);
 float intensity=envelope*radial*(.46+1.65*n)*bands*fine*pow(doppler,2.4);
 return heat(temp)*intensity*(2.8+uDeep*1.4);
}
vec3 starField(vec3 dir){
 vec2 uv=vec2(atan(dir.z,dir.x)/(2.*PI),asin(clamp(dir.y,-1.,1.))/PI);
 vec3 col=vec3(0.0014,0.0013,0.0018);
 for(int k=0;k<2;k++){float grid=k==0?360.:710.;vec2 cell=uv*grid,ip=floor(cell),fp=fract(cell)-.5;float rnd=hash(ip+float(k)*27.);float d=length(fp);float star=exp(-d*d*(k==0?850.:1600.))*step(k==0?.994:.9975,rnd);col+=vec3(.65,.59,.69)*star*(.22+rnd*.4);}
 float haze=pow(turbulence(uv*16.+vec2(.4,2.)),4.);col+=vec3(.008,.005,.011)*haze;
 return col;
}
void main(){
 vec2 uv=(gl_FragCoord.xy-uCenter*uResolution)/uResolution.y;
 float roll=-.075;uv=mat2(cos(roll),-sin(roll),sin(roll),cos(roll))*uv;
 float mass=.82+uDeep*.07;
 float cameraDistance=mix(29.,25.5,smoothstep(0.,1.,uIntro))*(1.-uDeep*.095);
 float elevation=.135+uPointer.y*.03;
 float azimuth=uPointer.x*.055;
 vec3 camera=vec3(sin(azimuth)*cameraDistance,sin(elevation)*cameraDistance,cos(azimuth)*cameraDistance);
 vec3 forward=normalize(-camera),right=normalize(cross(forward,vec3(0.,1.,0.))),up=cross(right,forward);
 vec3 ray=normalize(forward+right*uv.x*1.16+up*uv.y*1.16);
 vec3 p=camera,v=ray,emission=vec3(0.);
 float impact=length(cross(camera,ray)),h2=impact*impact,attenuation=1.;bool captured=false;
 // Keep the original 180-step rich path. Lower tiers cover the same ray distance.
 for(int k=0;k<180;k++){
  if(k>=uSteps)break;
  float r=length(p);
  if(r<2.*mass){captured=true;break;}
  if(r>44.)break;
  float ds=clamp(r*.075,.075,.75)*(180./float(uSteps));
  vec3 old=p;
  vec3 accel=-3.*mass*h2*p/pow(r,5.);
  v+=accel*ds;p+=v*ds;
  if(old.y*p.y<0.){
   float t=old.y/(old.y-p.y);vec3 hit=mix(old,p,t);
   vec3 light=disk(hit,normalize(v),mass);
   float edge=1./max(.28,abs(v.y)*4.5);
   emission+=light*edge*attenuation;
   attenuation*=.68;
  }
 }
 vec3 background=captured?vec3(0.):starField(normalize(v));
 vec3 color=emission+background*attenuation;
 float critical=5.19615*mass,ring=exp(-pow((impact-critical)/.028,2.));
 float shoulder=exp(-abs(impact-critical)/.18)*.045;
 float topWeight=.6+.4*smoothstep(-.1,.3,uv.y);
 color+=vec3(1.,.94,.86)*(ring*.64+shoulder)*topWeight;
 float haze=exp(-abs(impact-critical)/1.5)*.008;
 color+=vec3(.75,.46,.24)*haze;
 color=vec3(1.)-exp(-color*.98);
 color=pow(max(color,0.),vec3(.86));
 float vignette=1.-smoothstep(.28,1.02,length((gl_FragCoord.xy/uResolution-.5)*vec2(1.,.8)))*.35;
 color*=vignette;
 float grain=(hash(gl_FragCoord.xy+mod(floor(uTime*24.),97.))-.5)/255.;
 color=max(color+grain,0.);
 outColor=vec4(color,1.);
}`
