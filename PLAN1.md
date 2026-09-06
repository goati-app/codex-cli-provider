# PLAN1 — Librería de Codex CLI como proveedor de IA

Estado: implementación realizada y módulo público versionado. Versión actual: `v0.1.1`.

## Objetivo

Extraer la integración de Codex CLI que utiliza GOATI a una librería independiente y generalista, reutilizable por cualquier aplicación. Compartir únicamente la ejecución y la interpretación del protocolo; cada aplicación conserva sus prompts, reglas de negocio, perfiles, persistencia, monitoreo y límites.

Nombre de trabajo: `codex-cli-provider`. El nombre definitivo del módulo y su repositorio se decidirán al implementarlo.

## Decisión de alcance

La primera versión será una librería Go, tomando como base el código existente y el contrato real de GOATI. La API pública debe permanecer independiente del dominio. Para consumidores que no puedan importar Go directamente se ofrece, como opción de empaquetado, un ejecutable one-shot sobre stdin/stdout; esto no amplía el alcance a un servicio HTTP.

No construir un gateway, dashboard ni aplicación central. No incorporar clientes de OpenAI u OpenRouter. No extraer todo el sistema LLM de GOATI.

## Responsabilidades

| Librería | Aplicaciones consumidoras |
|---|---|
| Localizar e invocar el ejecutable de Codex | Decidir cuándo y para qué usar IA |
| Usar una ubicación explícita de autenticación | Aprovisionar Codex, credenciales y almacenamiento persistente |
| Preparar y limpiar el entorno de ejecución | Construir prompts e instrucciones |
| Enviar opciones de modelo, razonamiento y esquema de salida | Seleccionar perfiles y políticas de negocio |
| Interpretar eventos y resultado final | Validar el resultado de dominio |
| Devolver usage reportado, duración e identificadores disponibles | Guardar intentos y relacionarlos con operaciones propias |
| Cancelar y terminar procesos y descendientes | Decidir reintentos, fallback y presupuesto |
| Informar errores identificables y resultados parciales | Aplicar límites globales y coordinación entre réplicas |

GOATI podrá relacionar una ejecución con un scrape, tienda, producto o evaluación. Otros consumidores podrán relacionarla con sus propias entidades u operaciones. Ninguno de esos conceptos debe aparecer en los tipos de la librería.

## Base existente que se debe revisar

Referencias relativas a esta carpeta:

- `../backend/internal/llm/codex_transport.go`: ejecución, aislamiento, timeout, cancelación y limpieza de procesos.
- `../backend/internal/codexprotocol/jsonl.go`: interpretación de la salida JSONL y validaciones del protocolo.
- `../backend/internal/codexprotocol/runtime.go`: utilidades del entorno de ejecución.
- Tests asociados a esos archivos: conservar los casos útiles al independizar el código.
- `../backend/internal/matcher/ai.go`: contratos actuales de solicitud y resultado; sirven como referencia, no como dependencia.

Los tres archivos de ejecución/protocolo sumaban 777 líneas al revisar esta propuesta. La cifra incluye código específico de GOATI y no representa una estimación de líneas extraíbles ni de esfuerzo.

El controlador de límites, las reservas, la contabilidad persistente y los perfiles existentes en GOATI permanecerán en GOATI. Su integración con la librería se realizará mediante un adaptador propio del consumidor.

## Contrato propuesto

La API concreta se definirá después de revisar ambos consumidores. Debe permitir una operación de ejecución con contexto de cancelación, una solicitud y un resultado acompañado de un error.

### Configuración del ejecutor

- Ruta del ejecutable de Codex.
- Ruta explícita de `CODEX_HOME`, sin iniciar sesión ni copiar credenciales automáticamente.
- Directorio padre para archivos temporales.
- Timeout y límites de retención de salida.
- Opciones de ejecución y herramientas con valores predeterminados restrictivos y documentados.
- Registro técnico opcional, sin contenido de prompts, credenciales o salida cruda por defecto.

La configuración no debe modificar las variables globales del proceso anfitrión. Las variables necesarias se suministran al proceso hijo.

### Solicitud

- Prompt e instrucciones necesarias, conservando su semántica.
- Modelo y esfuerzo de razonamiento cuando sean aplicables.
- Esquema JSON opcional y su nombre cuando el protocolo lo requiera.
- Capacidades o herramientas soportadas de forma explícita.

No exponer argumentos arbitrarios del shell como mecanismo general de extensión. Invocar el ejecutable mediante argumentos estructurados. Rechazar opciones incompatibles o no soportadas con un error claro.

### Resultado

- Salida final.
- Modelo e identificador de hilo reportados, si existen.
- Tokens reportados, distinguiendo valor desconocido de cero.
- Duración de ejecución y, si existe una cola local, tiempo de espera separado.
- Metadatos disponibles de herramientas, sin inventar equivalencias entre eventos y consumo real.
- Información parcial recuperable cuando hay un fallo.

No convertir tokens de una suscripción de ChatGPT en un costo API supuesto. La aplicación decide cómo contabilizar o presentar el costo.

### Intentos y errores

Una llamada a la librería representa un intento de ejecución de Codex. No implica que internamente Codex haga una sola consulta al modelo.

La librería no realiza reintentos ni fallback automáticos. Debe distinguir, como mínimo, configuración inválida, capacidad no soportada, fallo de inicio, cancelación, timeout, fallo del proceso y salida/protocolo inválidos. Clasificar fallos de autenticación o límites del proveedor solo cuando exista evidencia suficiente; preservar una categoría desconocida cuando no sea posible identificarlos.

Los errores no deben impedir devolver usage u otros datos parciales ya conocidos. Esto permite que la aplicación registre un intento fallido que sí consumió recursos.

## Adaptabilidad y concurrencia

- Evitar dependencias de GOATI y estado global compartido entre instancias del ejecutor.
- GOATI actualmente serializa el transporte de Codex mediante un canal global de capacidad uno para controlar memoria. Preservar ese comportamiento en su adaptación inicial.
- Si se incluye control de concurrencia en la librería, hacerlo explícito y local al ejecutor, con cancelación durante la espera.
- No prometer coordinación entre procesos, contenedores o proyectos. Esa coordinación corresponde al consumidor o a infraestructura compartida.
- No aumentar el paralelismo sobre un mismo `CODEX_HOME` sin validar el comportamiento de la versión soportada de Codex y la gestión de sus credenciales.
- Primera versión centrada en ejecuciones independientes. Hilos persistentes, reanudación y streaming público se incorporarán solo si un consumidor concreto los necesita.
- No adoptar App Server durante la extracción inicial; evaluarlo después como un cambio independiente si aporta valor a casos reales.

## Etapas de implementación

### 1. Confirmar el contrato del consumidor inicial

Revisar los flujos reales de GOATI. Confirmar lenguaje, entrada, resultado esperado, herramientas, ejecución independiente o conversación persistente y forma de registrar intentos.

Resultado: contrato mínimo generalista de la librería y mapeo de los campos que GOATI necesita, sin trasladar conceptos de su dominio.

### 2. Extraer ejecución y protocolo

Crear el módulo Go con una estructura pequeña. Trasladar y adaptar el código de procesos y JSONL sin importar paquetes internos de GOATI. Separar las validaciones genéricas del protocolo de las restricciones propias de matching o búsqueda de tiendas.

Documentar requisitos del ejecutable de Codex y sistemas operativos soportados. El código actual utiliza grupos de procesos Unix; no declarar soporte multiplataforma sin implementarlo y verificarlo.

Resultado: librería ejecutable contra un Codex simulado y tests portados relevantes.

### 3. Integrar GOATI

Sustituir la implementación interna por un adaptador a la librería. Conservar perfiles, prompts, esquemas, límites, reservas, registros de intentos y decisiones de fallback actuales.

Preservar las restricciones actuales de herramientas, la serialización y la recuperación de consumo parcial. Eliminar la implementación duplicada solo después de verificar la equivalencia.

Resultado: GOATI consume la librería sin cambios intencionales en el comportamiento del producto.

### 4. Documentar y preparar el versionado

Añadir ejemplos mínimos de ejecución, salida estructurada, cancelación y registro de usage desde el consumidor. Documentar autenticación externa, almacenamiento persistente, concurrencia, errores y compatibilidad con Codex.

La versión inicial fue `v0.1.0`; `v0.1.1` actualiza la documentación de distribución pública. El contrato puede evolucionar antes de `v1`. El módulo se distribuye desde `github.com/goati-app/codex-cli-provider`; GOATI consume la versión publicada sin depender de una ruta local.

## Validación

Priorizar tests con ejecutables simulados y fixtures JSONL; no depender de consultas pagadas ni de credenciales reales para las verificaciones habituales.

Casos relevantes:

- Resultado válido y salida estructurada.
- Protocolo incompleto, malformado o superior a los límites permitidos.
- Usage ausente, reportado y parcial en un fallo.
- Timeout y cancelación antes de iniciar y durante la ejecución.
- Terminación de procesos descendientes y cierre de pipes sin bloqueos.
- Limpieza del entorno temporal y ausencia de filtración de credenciales en errores y logs.
- Opciones no soportadas y restricciones de herramientas.
- Espera cancelable cuando exista control de concurrencia local.
- Adaptación de GOATI: mismo registro de intentos, límites y comportamiento de fallback, sin reintentos adicionales introducidos por la librería.

Las pruebas reales con Codex serán explícitas, acotadas y separadas de los tests habituales. Deben comprobar la versión y las capacidades que se declaren soportadas.

## Criterios de finalización

- La librería no importa código ni tipos de negocio de GOATI ni de ningún consumidor.
- GOATI la usa en sus flujos reales mediante un adaptador propio.
- Cualquier consumidor conserva sus propias relaciones entre ejecución y operación de negocio.
- GOATI mantiene sus controles actuales y deja de duplicar el transporte extraído.
- Usage desconocido y fallos con consumo parcial se representan correctamente.
- Reintentos, fallback, presupuestos y persistencia siguen siendo decisiones explícitas de cada aplicación.
- La compatibilidad, los límites y los ejemplos de uso están documentados y verificados.
