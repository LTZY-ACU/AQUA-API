/**
 * Textos de la página principal (español): héroe / barra de especificaciones / capacidades / inicio rápido / precios de modelos / decisiones de diseño / FAQ / CTA.
 *
 * 意图（Why）：
 *   首页文案量大、改版频繁，从 site.ts 拆出独立文件，便于单独迭代与六语翻译对齐。
 *
 * 流转（Flow）：
 *   ./home.ts → locales/<lang>/index.ts 聚合进 site 命名空间 → i18n/index.ts 的 messages
 *   → app/page.tsx 以 t('site.home.*') 取用（本文件只含 site.home. 之下的层级）。
 *
 * 扩展（Extend）：
 *   新增键时六种语言的同一路径必须同步补齐（六文件键结构完全一致）；
 *   专有名词保留英文：OpenAI / LLM / API / base_url / token / GitHub / MIT。
 */
export default {
  hero: {
    tagline: 'Pasarela LLM autoalojada · despliegue en un solo binario',
    titleLine1: 'Una única API compatible con OpenAI,',
    titleLine2: 'reúne decenas de proveedores.',
    desc: 'Protocolos como OpenAI / Anthropic / Gemini se unifican aquí en un formato compatible con OpenAI. Facturación, registros, grupos de claves y reintentos funcionan desde el primer momento. Toda la pasarela es de código abierto: despliega la tuya cuando quieras.',
    terminalUserMessage: 'Hola',
    terminalAssistantReply: '¡Hola! ¿En qué puedo ayudarte?',
  },
  action: {
    console: 'Abrir la consola',
    register: 'Registrarse y obtener un token',
    browseModels: 'Ver modelos y precios',
    readSource: 'Leer el código fuente',
  },
  stats: {
    modelsOnline: 'Modelos en línea',
    channelTypes: 'Tipos de canal upstream',
    protocols: 'Protocolos compatibles',
    deployFiles: 'Archivos para desplegar',
  },
  features: {
    title: 'Capacidades esenciales',
    desc: 'Todo lo que necesita una pasarela está incluido: conversión de protocolos, facturación, gestión de claves, registro y tolerancia a fallos — sin ensamblar componentes de terceros.',
    f1Title: 'Protocolos unificados',
    f1Desc: 'Conversión entre protocolos entrantes y salientes como OpenAI / Anthropic / Gemini, exponiendo solo un formato compatible con OpenAI.',
    f2Title: 'Facturación precisa',
    f2Desc: 'Modos por token, por llamada y gratuito, con precios configurados por grupo: cada cargo puede verificarse línea por línea.',
    f3Title: 'Grupos de claves y reintentos',
    f3Desc: 'Se rotan varias claves por canal; si una falla, la siguiente toma el relevo automáticamente, de modo que los fallos puntuales del proveedor pasan desapercibidos.',
    f4Title: 'Registro completo',
    f4Desc: 'El modelo, el número de tokens, la latencia y el código de estado de cada llamada quedan registrados: el uso siempre es auditable.',
    f5Title: 'Conmutación por error',
    f5Desc: 'Disyuntor a nivel de canal con espera de enfriamiento: cuando toda una ruta cae, las peticiones reintentan automáticamente en la siguiente.',
    f6Title: 'Autoalojamiento en un solo binario',
    f6Desc: 'Un binario de Go más SQLite, con el frontend integrado: el despliegue solo necesita un archivo.',
  },
  quickstart: {
    title: 'Empieza en tres pasos',
    desc: 'Solo se expone una API compatible con OpenAI, así que tu SDK actual solo necesita una nueva base_url.',
    s1Title: 'Regístrate y crea un token',
    s1Desc: 'Genera un token de acceso en la consola y define de paso su presupuesto y sus modelos permitidos.',
    s2Title: 'Sustituye la base_url',
    s2Desc: 'En cualquier cliente compatible con el SDK de OpenAI, apunta base_url al endpoint /v1 de este sitio.',
    s3Title: 'Mantén tu código actual',
    s3Desc: 'Los protocolos son compatibles: las estructuras de petición y respuesta no cambian y no hay que tocar el código de negocio.',
    sdkNote: '# Python / Node: cambia la base_url del SDK de OpenAI por {base}',
    tokenPlaceholder: 'tu-token',
    sampleMessage: 'Hola',
  },
  models: {
    title: 'Modelos y precios',
    descAgent: 'Estás viendo tu nivel mayorista de agente: los precios tachados son los de lista y el naranja es tu precio con descuento.',
    descPublic: 'Datos en vivo del sitio; los precios se muestran por grupo, sin adornos.',
    colModel: 'Modelo',
    colGroup: 'Grupo',
    colPrice: 'Precio',
    viewAllCount: 'Ver los {n} modelos y el precio completo',
    viewAll: 'Ver todos los modelos y el precio completo',
    priceTbd: 'Por definir',
    priceFree: 'Gratis',
    pricePerCall: 'Por llamada',
    pricePerToken: 'Por token',
  },
  design: {
    title: 'Decisiones de diseño y costes',
    desc: 'Sin caja negra: las reglas de precios, el historial de uso y los límites de coste están a la vista.',
    principlesLabel: 'Principios',
    costsLabel: 'Desglose de costes',
    p1Title: 'Precios explícitos',
    p1Desc: 'El precio unitario de cada modelo figura en la plaza de modelos, en modos por llamada / por token / gratuito: cada factura puede verificarse línea por línea.',
    p2Title: 'Tú controlas tu uso',
    p2Desc: 'El modelo, los tokens, la latencia y el código de estado de cada llamada se registran y siempre puedes consultarlos — y yo no puedo modificarlos.',
    p3Title: 'El código es abierto',
    p3Desc: 'Toda la pasarela está publicada en GitHub bajo licencia MIT; si algún día lo dejo, aún podrás desplegar la tuya.',
    c1Label: 'Servidor',
    c1Value: 'Fijo mensual · un servidor dedicado ejecuta la pasarela y la base de datos',
    c2Label: 'Ancho de banda y tráfico',
    c2Value: 'Variable · cuanto más uso, mayor',
    c3Label: 'Coste de los modelos upstream',
    c3Value: 'Según uso · lo que pago al proveedor es la base de coste',
    c4Label: 'Dominio y certificado',
    c4Value: 'Un importe pequeño al año',
    costNotePrefix:
      'Lo que pagas cubre primero los costes, y solo el excedente es mi motivo para seguir manteniéndolo. Si algún día de verdad no cubre gastos, lo explicaré en los anuncios, ',
    costNoteBold: 'en lugar de subir los precios en silencio',
    costNoteSuffix: '.',
  },
  faq: {
    title: 'Preguntas frecuentes',
    q1: '¿Este sitio seguirá abierto siempre?',
    a1: 'Haré todo lo posible. Sus costes son manejables, no dependo de él para ganar dinero y no hay un problema de «se acaba la financiación y desaparece». Si algún día tuviera que cerrar, lo anunciaría con antelación y daría una guía completa de autoalojamiento.',
    q2: '¿Por qué publicarlo como código abierto?',
    a2: 'Primero, para que puedas comprobar que todo lo que digo es cierto; segundo, para que si algún día lo dejo, este proyecto no desaparezca conmigo. La licencia es MIT.',
    q3: '¿Se cobrarán los modelos del grupo gratuito?',
    a3: 'El grupo gratuito simplemente no se factura — no juego al «gratis primero, de pago cuando ya estés enganchado». El alcance gratuito puede cambiar con los precios upstream, pero avisaré de cualquier cambio con antelación.',
    q4: '¿Se usarán mis datos de llamadas?',
    a4: 'No. Los registros solo se usan para facturación y diagnóstico, y se guardan en mi propio servidor. Así lo indica la política de privacidad del sitio.',
    q5: '¿Qué pasa si el upstream es inestable?',
    a5: 'Grupos de claves + reintentos automáticos + espera de enfriamiento: si una clave falla, entra la siguiente; si todo un canal falla, cambiamos de canal y reintentamos. Tú sigues enviando una sola petición.',
    q6: '¿Cómo te contacto?',
    a6: 'Puedes encontrarme en «Contacto» y «Reportar abuso» al pie de la página. Escríbeme sin más si necesitas algo.',
  },
  cta: {
    titleLoggedIn: 'Empieza con un token nuevo.',
    titleGuest: 'Empieza con un token.',
    descLoggedIn: 'Conéctalo a tu código actual, completa una petición y decide si te quedas.',
    descGuest: 'Registrarse es gratis: completa una petición y decide si te quedas.',
  },
}
