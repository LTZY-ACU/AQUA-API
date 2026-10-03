/**
 * 法律 / 信息页词条（Español）：用户协议、隐私政策、安全致谢、联系方式、
 * 加入交流群、投诉举报、安装向导七个页面共用本域，键按页面分组。
 *
 * 意图（Why）：
 *   七个合规 / 信息页文案量大（条款正文属法律文案）且更新节奏独立，单独成域
 *   便于集中审校与翻译；各语言必须完整、正式，保持条款编号与结构不变。
 *
 * 流转（Flow）：
 *   ./legal.ts → locales/<lang>/index.ts 聚合到 site.legal → 页面 t('site.legal.<页面>.*')
 *
 * 扩展（Extend）：
 *   新增页面分组：六个语言的 legal.ts 同步加同名分组（键集合必须一致）；
 *   新增条款：保持 s<N>Title / s<N>Body 编号连续，六语言同步补齐。
 */
export default {
  terms: {
    title: 'Términos de servicio',
    updatedAt: 'Última actualización: 2026-09-30',
    s1Title: '1. Disposiciones generales',
    s1Body:
      'Este servicio es un servicio de retransmisión de API LLM prestado por el operador de este sitio. Al usarlo confirmas que has leído y aceptas todas las condiciones de este acuerdo; si no estás de acuerdo, deja de usar el servicio.',
    s2Title: '2. Cuenta y seguridad',
    s2Body:
      'Debes custodiar bien tu cuenta y tus tokens de acceso, y no prestarlos, cederlos ni darles un uso malintencionado. Las consecuencias de una mala custodia son tuyas. Si detectas que tu cuenta ha sido comprometida, contacta de inmediato con un administrador.',
    s3Title: '3. Saldo y facturación',
    s3Body:
      'El saldo se expresa en RMB; las recargas sirven para compensar los costes de llamadas a la API y no constituyen ninguna moneda virtual ni derecho transferible. El saldo es de uso personal y no puede cederse ni retirarse. Las reglas de facturación siguen la plaza de modelos publicada, y cualquier cambio de precio se anunciará de antemano en el sitio.',
    s4Title: '4. Declaración sobre capacidades upstream basadas en cuentas de suscripción',
    s4Body1:
      'Este sitio puede conectar capacidades upstream acreditadas por cuentas de suscripción de terceros, con el fin de ampliar los modelos y las rutas disponibles. Estas capacidades solo se ofrecen con fines de aprendizaje, investigación y validación técnica; complementan los servicios existentes y no constituyen ningún compromiso comercial ni autorización de reventa.',
    s4Body2:
      'Al usar estas capacidades, debes asegurarte por tu cuenta de cumplir los términos de servicio de los proveedores upstream correspondientes y la legislación de tu jurisdicción; no debes usarlas para reventa comercial, extracción masiva, elusión de restricciones o cuotas de los proveedores, ni para ningún fin fuera del alcance autorizado.',
    s4Body3:
      'Todas las consecuencias derivadas de tu incumplimiento de lo anterior o de los términos de los proveedores upstream (incluidos, entre otros, la restricción de cuentas upstream, la interrupción del servicio, disputas o responsabilidad legal) son tuyas, y este sitio no asume responsabilidad solidaria. Este sitio solo ofrece acceso técnico y no garantiza la disponibilidad ni la legalidad de las cuentas upstream.',
    s4Body4:
      'Este sitio se reserva el derecho de suspender o cancelar los servicios correspondientes y de restringir o desactivar las cuentas pertinentes cuando reciba quejas de proveedores upstream, requerimientos regulatorios o detecte usos indebidos.',
    s5Title: '5. Cumplimiento de contenidos',
    s5Body:
      'No debes generar ni difundir contenido ilegal o no conforme a través de este servicio (incluidos, entre otros, la amenaza a la seguridad nacional, la pornografía o la violencia, o la vulneración de derechos de terceros). Este sitio cuenta con filtrado de palabras sensibles y mecanismos de seguridad de contenidos; el contenido infractor será rechazado y podrá conllevar acciones sobre la cuenta.',
    s6Title: '6. Disponibilidad del servicio',
    s6Body:
      'Este servicio se esfuerza por funcionar de forma estable, pero no se compromete a una disponibilidad absoluta. Ante una indisponibilidad causada por fallos de proveedores upstream, fluctuaciones de red o mantenimiento programado, este sitio hará lo posible por restablecerlo, pero no asume las pérdidas indirectas derivadas.',
    s7Title: '7. Exención de responsabilidad',
    s7Body:
      'Este sitio no se responsabiliza de las pérdidas causadas por fuerza mayor, ataques informáticos, fallos del sistema, interrupciones de servicios de terceros u otras causas ajenas al sitio. Consulta la Exención de responsabilidad completa para una exposición más detallada.',
    s8Title: '8. Cambios en el acuerdo',
    s8Body:
      'Este sitio puede revisar este acuerdo según sus necesidades operativas; toda revisión se publicará en el sitio. Seguir usándolo implica aceptar las condiciones revisadas.',
  },
  privacy: {
    title: 'Política de privacidad',
    updatedAt: 'Última actualización: 2026-09-30',
    s1Title: '1. Qué información recopilamos',
    s1Body:
      'La información de cuenta recogida en el registro (usuario, correo, hash de contraseña); el contenido de las peticiones y la información de uso en los registros de llamadas; y la información necesaria de dispositivo y red (para seguridad y control de riesgos).',
    s2Title: '2. Para qué se usa la información',
    s2Body:
      'Para prestar y mantener el servicio, gestionar la facturación, diagnosticar fallos, garantizar la seguridad y realizar auditorías de cumplimiento. No vendemos ni alquilamos tu información personal a terceros.',
    s3Title: '3. Cómo se almacena la información',
    s3Body:
      'Los datos se almacenan en los servidores propios de este sitio (almacenamiento local SQLite por defecto). Las contraseñas se guardan como hashes con sal, nunca en texto plano. Los tokens de acceso en texto plano solo se muestran una vez, al crearse.',
    s4Title: '4. Tus derechos',
    s4Body:
      'Tienes derecho a acceder, corregir o eliminar la información de tu cuenta y los datos relacionados. Para eliminar tu cuenta, contacta con un administrador. Cumplimiento de contenidos: el contenido de peticiones que infrinja la ley lo conservaremos según lo exija la ley y colaboraremos con los reguladores.',
    s5Title: '5. Protección de menores',
    s5Body:
      'Este servicio no se dirige a menores de 18 años. Si eres tutor y detectas que un menor usa este servicio, contacta con nosotros.',
    s6Title: '6. Cambios en esta política',
    s6Body:
      'Si esta política cambia de forma sustancial, la publicaremos de manera destacada en el sitio. Seguir usándolo implica aceptar la política actualizada.',
  },
  security: {
    title: 'Agradecimientos de seguridad',
    updatedAt: 'Última actualización: 2026-09-30',
    s1Title: 'Informar de una vulnerabilidad',
    s1Body:
      'Si encuentras una vulnerabilidad de seguridad en este sitio, agradecemos una divulgación responsable: no difundas los detalles públicamente; contacta primero con un administrador por correo o por el grupo, y te agradeceremos públicamente tras corregirla.',
    s2Title: 'Contenido del informe',
    s2Body:
      'Describe: el tipo de vulnerabilidad, su alcance, los pasos de reproducción (lo más concisos posible) y la corrección propuesta. No realices pruebas destructivas.',
    s3Title: 'Lista de agradecimientos',
    s3Body: 'Agradecemos a los siguientes investigadores su contribución a la seguridad de este sitio:',
    thanksRevealed: '(La lista se publicará aquí cuando se reciban informes válidos)',
    thanksShow: 'Ver investigadores agradecidos',
  },
  contact: {
    title: 'Contacto',
    updatedAt: 'Última actualización: 2026-09-30',
    emailTitle: 'Correo de soporte',
    emailBody: 'Para consultas comerciales, colaboraciones o ayuda, escríbenos a:',
    emailNotSet: '(Aún no hay un correo público configurado; contáctanos a través del grupo comunitario)',
    groupTitle: 'Grupo comunitario',
    groupBodyPrefix: 'Si tienes problemas de uso, únete al grupo comunitario para obtener ayuda: ',
    groupLink: 'Únete a la comunidad',
    groupBodySuffix: '.',
    hoursTitle: 'Horario de respuesta',
    hoursBody:
      'Procuramos responder los correos en un plazo de 48 horas. Para asuntos urgentes (como una cuenta comprometida), avisa también a un administrador por el grupo comunitario.',
  },
  join: {
    title: 'Únete a la comunidad',
    subtitle: '¿Tienes una duda, una idea o simplemente ganas de hablar de modelos? Únete a nosotros.',
    groupMainName: 'Grupo principal de LTZY-API',
    groupMainDesc: 'Se comentan dudas de uso, la integración de canales y los avances de nuevas funciones.',
    joinButton: 'Unirse',
  },
  report: {
    title: 'Reportar abuso',
    updatedAt: 'Última actualización: 2026-09-30',
    s1Title: 'Alcance',
    s1Body:
      '1) Quejas sobre la calidad del servicio de este sitio; 2) contenido publicado por este sitio o sus usuarios que se sospeche ilegal o no conforme; 3) problemas de seguridad como cuentas comprometidas o mal utilizadas.',
    s2Title: 'Cómo informar',
    s2BodyPrefix: 'Envía tus informes por correo electrónico:',
    emailNotSet: '(Aún no hay un correo público para informes configurado)',
    s2BodySuffix:
      '. Adjunta si es posible las pruebas pertinentes (capturas, horas, contenido implicado) para que podamos verificarlo con rapidez.',
    s3Title: 'Plazo de respuesta',
    s3Body:
      'Nos comprometemos a responder a los informes en un plazo de 48 horas y, tras verificarlos, a tomar las medidas necesarias conforme a la ley (retirar contenido, actuar sobre la cuenta, informar al regulador, etc.).',
    s4Title: 'Responsabilidad por denuncias falsas',
    s4Body: 'Nos reservamos el derecho a emprender acciones legales por informes deliberadamente falsos o malintencionados.',
  },
  install: {
    checking: 'Comprobando el estado de la instalación…',
    doneTitle: 'Instalación completada',
    doneDesc: 'Se ha configurado una cuenta de administrador. Ve al inicio de sesión del panel.',
    doneButton: 'Ir al acceso del panel',
    wizardBrand: 'Asistente de instalación',
    title: 'Crear un administrador',
    subtitle: 'Esta es la primera instalación; configura la cuenta de administrador del sitio',
    siteNameLabel: 'Nombre del sitio',
    siteNameHelp: 'Se muestra en el título de la página y el pie',
    siteNamePlaceholder: 'p. ej.: Pasarela LTZY-API',
    usernameLabel: 'Usuario administrador',
    passwordLabel: 'Contraseña de administrador',
    passwordHelp: 'Al menos 8 caracteres',
    passwordPlaceholder: 'Al menos 8 caracteres',
    confirmLabel: 'Confirmar contraseña',
    confirmPlaceholder: 'Vuelve a introducirla',
    submit: 'Finalizar instalación',
    haveAccount: '¿Ya tienes una cuenta?',
    loginLink: 'Iniciar sesión',
    errPasswordShort: 'La contraseña debe tener al menos 8 caracteres',
    errPasswordMismatch: 'Las dos contraseñas no coinciden',
    errFailed: 'No se pudo instalar',
  },
}
